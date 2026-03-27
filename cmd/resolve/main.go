package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/git-pkgs/registries"
	_ "github.com/git-pkgs/registries/all"
	"github.com/git-pkgs/resolve"
	_ "github.com/git-pkgs/resolve/parsers"
)

// purlTypeToManager maps PURL types to their default package manager.
var purlTypeToManager = map[string]string{
	"npm":       "npm",
	"gem":       "bundler",
	"cargo":     "cargo",
	"pypi":      "uv",
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

// registryToEcosystem maps common registry names to purl types.
var registryToEcosystem = map[string]string{
	"npmjs.org":             "npm",
	"rubygems.org":          "gem",
	"crates.io":             "cargo",
	"pypi.org":              "pypi",
	"proxy.golang.org":      "golang",
	"repo1.maven.org":       "maven",
	"packagist.org":         "composer",
	"pub.dev":               "pub",
	"hex.pm":                "hex",
	"nuget.org":             "nuget",
	"swiftpackageindex.com": "swift",
	"clojars.org":           "clojars",
	"hackage.haskell.org":   "hackage",
	"anaconda.org":          "conda",
	"cocoapods.org":         "cocoapods",
	"conan.io":              "conan",
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
	registry := flag.String("registry", "", "registry name (e.g. rubygems.org)")
	ecosystem := flag.String("ecosystem", "", "ecosystem/purl type (e.g. gem, npm, cargo)")
	pkg := flag.String("package", "", "package name (required)")
	version := flag.String("version", "", "version (default: latest)")
	tree := flag.Bool("tree", false, "output dependency tree with PURLs")
	manager := flag.String("manager", "", "override package manager (e.g. uv instead of pip)")
	timeout := flag.Int("timeout", 120, "timeout in seconds")
	flag.Parse()

	if *pkg == "" {
		fatal("--package is required")
	}
	if *registry == "" && *ecosystem == "" {
		fatal("--registry or --ecosystem is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*timeout)*time.Second)
	defer cancel()

	// Determine the ecosystem and manager.
	eco := *ecosystem
	if eco == "" {
		eco = registryToEcosystem[*registry]
		if eco == "" {
			fatal("unsupported registry: %s", *registry)
		}
	}

	mgrName := *manager
	if mgrName == "" {
		mgrName = purlTypeToManager[eco]
		if mgrName == "" {
			fatal("unsupported ecosystem: %s", eco)
		}
	}

	// Create registry client.
	client := registries.DefaultClient()
	reg, err := registries.New(eco, "", client)
	if err != nil {
		fatal("creating registry client: %v", err)
	}

	// Fetch dependencies.
	deps, err := fetchDeps(ctx, reg, *pkg, *version)
	if err != nil {
		fatal("%v", err)
	}

	// Convert to InputDep format, filtering to runtime only.
	var inputDeps []resolve.InputDep
	for _, dep := range deps {
		if dep.Scope != registries.Runtime {
			continue
		}
		inputDeps = append(inputDeps, resolve.InputDep{
			Name:    dep.Name,
			Version: dep.Requirements,
		})
	}

	// No deps to resolve: return empty result.
	if len(inputDeps) == 0 {
		if *tree {
			fmt.Println("[]")
		} else {
			fmt.Println("{}")
		}
		os.Exit(0)
	}

	// Run resolution.
	result, err := resolve.ResolveDeps(ctx, mgrName, inputDeps)
	if err != nil {
		fatal("resolution failed: %v", err)
	}

	// Filter out the temp project from results (shows up as "resolve-*").
	result.Direct = filterTempProject(result.Direct)

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

// fetchDeps fetches runtime dependencies for a package version from the registry.
func fetchDeps(ctx context.Context, reg registries.Registry, pkg, version string) ([]registries.Dependency, error) {
	if version == "" || version == ">= 0" {
		// Find the latest version.
		latest, err := registries.FetchLatestVersion(ctx, reg, pkg)
		if err != nil {
			return nil, fmt.Errorf("fetching latest version of %s: %w", pkg, err)
		}
		if latest == nil {
			return nil, fmt.Errorf("no versions found for %s", pkg)
		}
		version = latest.Number
	}

	deps, err := reg.FetchDependencies(ctx, pkg, version)
	if err != nil {
		return nil, fmt.Errorf("fetching deps for %s@%s: %w", pkg, version, err)
	}
	return deps, nil
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

// filterTempProject removes the temporary project entry from results.
func filterTempProject(deps []*resolve.Dep) []*resolve.Dep {
	var filtered []*resolve.Dep
	for _, dep := range deps {
		if strings.HasPrefix(dep.Name, "resolve-") || strings.HasPrefix(dep.Name, "resolve_") {
			continue
		}
		filtered = append(filtered, dep)
	}
	return filtered
}

func fatal(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	errJSON, _ := json.Marshal(map[string]string{"error": msg})
	fmt.Fprintln(os.Stderr, string(errJSON))
	os.Exit(1)
}
