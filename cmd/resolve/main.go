package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	ecosystems "github.com/ecosyste-ms/ecosystems-go"
	"github.com/ecosyste-ms/ecosystems-go/packages"
	"github.com/git-pkgs/resolve"
	_ "github.com/git-pkgs/resolve/parsers"
)

// purlTypeToManager maps PURL types to their default package manager.
var purlTypeToManager = map[string]string{
	"npm":       "npm",
	"gem":       "bundler",
	"cargo":     "cargo",
	"pypi":      "pip",
	"golang":    "gomod",
	"maven":     "maven",
	"composer":  "composer",
	"pub":       "pub",
	"hex":       "mix",
	"nuget":     "nuget",
	"swift":     "swift",
	"clojars":   "lein",
	"hackage":   "stack",
	"conda":     "conda",
	"deno":      "deno",
	"helm":      "helm",
	"conan":     "conan",
	"cocoapods": "cocoapods",
}

// flatResult holds a flat name->version map.
type flatResult map[string]string

// treeDep is the tree output format.
type treeDep struct {
	PURL    string     `json:"purl"`
	Name    string     `json:"name"`
	Version string     `json:"version"`
	Deps    []*treeDep `json:"deps,omitempty"`
}

func main() {
	registry := flag.String("registry", "", "ecosyste.ms registry name (e.g. rubygems.org)")
	ecosystem := flag.String("ecosystem", "", "ecosystem/purl type (e.g. gem, npm, cargo)")
	pkg := flag.String("package", "", "package name (required)")
	version := flag.String("version", "", "version (default: latest)")
	tree := flag.Bool("tree", false, "output dependency tree with PURLs")
	before := flag.String("before", "", "only consider versions published before this date (ISO 8601)")
	manager := flag.String("manager", "", "override package manager (e.g. uv instead of pip)")
	timeout := flag.Int("timeout", 120, "timeout in seconds")
	apiKey := flag.String("api-key", "", "ecosyste.ms API key (also reads ECOSYSTEMS_API_KEY env)")
	flag.Parse()

	if *pkg == "" {
		fatal("--package is required")
	}
	if *registry == "" && *ecosystem == "" {
		fatal("--registry or --ecosystem is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*timeout)*time.Second)
	defer cancel()

	// Set up ecosyste.ms API client.
	key := *apiKey
	if key == "" {
		key = os.Getenv("ECOSYSTEMS_API_KEY")
	}
	var opts []ecosystems.Option
	if key != "" {
		opts = append(opts, ecosystems.WithAPIKey(key))
	}
	client, err := ecosystems.NewClient("resolve.ecosyste.ms/1.0", opts...)
	if err != nil {
		fatal("creating API client: %v", err)
	}

	// Determine the manager and registry to use.
	mgrName := *manager
	registryName := *registry

	if mgrName == "" || registryName == "" {
		// Fetch registries from the API to map registry name <-> purl type.
		registries, err := client.ListRegistries(ctx)
		if err != nil {
			fatal("fetching registries: %v", err)
		}

		if *ecosystem != "" && registryName == "" {
			// Find the default registry for this purl type.
			for _, reg := range registries {
				if reg.PurlType == *ecosystem && reg.Default {
					registryName = reg.Name
					break
				}
			}
			if registryName == "" {
				// Fall back to first matching registry.
				for _, reg := range registries {
					if reg.PurlType == *ecosystem {
						registryName = reg.Name
						break
					}
				}
			}
			if registryName == "" {
				fatal("no registry found for ecosystem %s", *ecosystem)
			}
		}

		if mgrName == "" {
			if *ecosystem != "" {
				mgrName = purlTypeToManager[*ecosystem]
			} else {
				// Look up the purl type for this registry.
				for _, reg := range registries {
					if reg.Name == registryName {
						mgrName = purlTypeToManager[reg.PurlType]
						break
					}
				}
			}
			if mgrName == "" {
				fatal("cannot determine package manager for registry %s", registryName)
			}
		}
	}

	// Fetch version and its dependencies.
	deps, err := fetchDeps(ctx, client, registryName, *pkg, *version, *before)
	if err != nil {
		fatal("%v", err)
	}

	// Convert to InputDep format.
	var inputDeps []resolve.InputDep
	for _, dep := range deps {
		inputDeps = append(inputDeps, resolve.InputDep{
			Name:    dep.PackageName,
			Version: deref(dep.Requirements),
		})
	}

	// Run resolution.
	result, err := resolve.ResolveDeps(ctx, mgrName, inputDeps)
	if err != nil {
		fatal("resolution failed: %v", err)
	}

	// Format output.
	var output any
	if *tree {
		output = toTreeDeps(result.Direct)
	} else {
		output = toFlat(result.Direct)
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(output); err != nil {
		fatal("encoding output: %v", err)
	}
}

// fetchDeps fetches runtime dependencies for a package version from the ecosyste.ms API.
func fetchDeps(ctx context.Context, client *ecosystems.Client, registry, pkg, version, before string) ([]packages.Dependency, error) {
	if version != "" && version != ">= 0" {
		ver, err := client.GetVersion(ctx, registry, pkg, version)
		if err != nil {
			return nil, fmt.Errorf("fetching version %s@%s: %w", pkg, version, err)
		}
		if ver == nil {
			return nil, fmt.Errorf("version %s@%s not found", pkg, version)
		}
		return runtimeDeps(ver.Dependencies), nil
	}

	// No specific version: get all versions and pick the latest.
	versions, err := client.GetAllVersions(ctx, registry, pkg)
	if err != nil {
		return nil, fmt.Errorf("fetching versions for %s: %w", pkg, err)
	}
	if len(versions) == 0 {
		return nil, fmt.Errorf("no versions found for %s", pkg)
	}

	// Filter by before date if specified.
	if before != "" {
		beforeTime, err := time.Parse(time.RFC3339, before)
		if err != nil {
			beforeTime, err = time.Parse("2006-01-02", before)
			if err != nil {
				return nil, fmt.Errorf("invalid --before date: %w", err)
			}
		}
		var filtered []packages.Version
		for _, v := range versions {
			if v.PublishedAt != nil {
				pub, err := time.Parse(time.RFC3339, *v.PublishedAt)
				if err == nil && pub.Before(beforeTime) {
					filtered = append(filtered, v)
				}
			}
		}
		if len(filtered) == 0 {
			return nil, fmt.Errorf("no versions of %s found before %s", pkg, before)
		}
		versions = filtered
	}

	latest := versions[0].Number

	ver, err := client.GetVersion(ctx, registry, pkg, latest)
	if err != nil {
		return nil, fmt.Errorf("fetching version %s@%s: %w", pkg, latest, err)
	}
	if ver == nil {
		return nil, fmt.Errorf("version %s@%s not found", pkg, latest)
	}
	return runtimeDeps(ver.Dependencies), nil
}

// runtimeDeps filters to only runtime dependencies.
func runtimeDeps(deps []packages.Dependency) []packages.Dependency {
	var runtime []packages.Dependency
	for _, dep := range deps {
		kind := deref(dep.Kind)
		if kind == "" || strings.EqualFold(kind, "runtime") {
			runtime = append(runtime, dep)
		}
	}
	return runtime
}

// toFlat converts a dependency tree to a flat name->version map.
func toFlat(deps []*resolve.Dep) flatResult {
	result := make(flatResult)
	var walk func([]*resolve.Dep)
	walk = func(deps []*resolve.Dep) {
		for _, dep := range deps {
			if _, exists := result[dep.Name]; !exists {
				result[dep.Name] = dep.Version
			}
			if dep.Deps != nil {
				walk(dep.Deps)
			}
		}
	}
	walk(deps)
	return result
}

// toTreeDeps converts resolve.Dep to the JSON-friendly tree format.
func toTreeDeps(deps []*resolve.Dep) []*treeDep {
	if deps == nil {
		return nil
	}
	result := make([]*treeDep, 0, len(deps))
	for _, dep := range deps {
		td := &treeDep{
			PURL:    dep.PURL,
			Name:    dep.Name,
			Version: dep.Version,
			Deps:    toTreeDeps(dep.Deps),
		}
		result = append(result, td)
	}
	return result
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func fatal(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	errJSON, _ := json.Marshal(map[string]string{"error": msg})
	fmt.Fprintln(os.Stderr, string(errJSON))
	os.Exit(1)
}
