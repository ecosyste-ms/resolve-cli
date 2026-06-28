package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/git-pkgs/managers/definitions"
	"github.com/git-pkgs/purl"
	"github.com/git-pkgs/registries"
	_ "github.com/git-pkgs/registries/all"
	"github.com/git-pkgs/resolve"
	_ "github.com/git-pkgs/resolve/parsers"
)

const defaultTimeoutSeconds = 120

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
	timeout := flag.Int("timeout", defaultTimeoutSeconds, "timeout in seconds")
	flag.Parse()

	if *pkg == "" {
		fatal("--package is required")
	}
	if *registry == "" && *ecosystem == "" {
		fatal("--registry or --ecosystem is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*timeout)*time.Second)
	defer cancel()

	purlType := derivePURLType(*ecosystem, *registry)
	if purlType == "" {
		fatal("could not determine ecosystem for registry %q", *registry)
	}

	mgrName := *manager
	if mgrName == "" {
		mgrName = defaultManager(purlType)
		if mgrName == "" {
			fatal("no resolver available for ecosystem %q", purlType)
		}
	}

	// Create registry client.
	client := registries.DefaultClient()
	reg, err := registries.New(purlType, "", client)
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
		return
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

// derivePURLType returns the purl type for the given --ecosystem or --registry
// flag. The --ecosystem value may be either a purl type or an ecosystem name;
// both are normalised via purl.EcosystemToPURLType. Registry values (bare
// hostnames or URLs) are matched against the default URL for each ecosystem
// registered in git-pkgs/registries.
func derivePURLType(ecosystem, registry string) string {
	if ecosystem != "" {
		return purl.EcosystemToPURLType(ecosystem)
	}
	host := hostOf(registry)
	if host == "" {
		return ""
	}
	for _, eco := range registries.SupportedEcosystems() {
		if hostMatches(host, hostOf(registries.DefaultURL(eco))) {
			return eco
		}
	}
	return ""
}

// hostOf returns the hostname from a URL or bare host string.
func hostOf(s string) string {
	if s == "" {
		return ""
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// hostMatches reports whether two hosts refer to the same registry, allowing
// either side to be a subdomain of the other (e.g. npmjs.org matches
// registry.npmjs.org).
func hostMatches(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return a == b || strings.HasSuffix(a, "."+b) || strings.HasSuffix(b, "."+a)
}

// defaultManager returns the package manager to use for a purl type when
// --manager is not given. Candidates are manager definitions whose ecosystem
// maps to purlType and which have a parser registered in git-pkgs/resolve.
// When several qualify, the one whose name equals the purl type wins (npm,
// maven, composer, cargo, ...); otherwise the lowest detection priority is
// taken as the most generic tool, with name as a final tiebreak for
// determinism.
func defaultManager(purlType string) string {
	defs, err := definitions.LoadEmbedded()
	if err != nil {
		return ""
	}

	resolvers := make(map[string]bool)
	for _, m := range resolve.Managers() {
		resolvers[m] = true
	}

	var best *definitions.Definition
	for _, def := range defs {
		if purl.EcosystemToPURLType(def.Ecosystem) != purlType {
			continue
		}
		if !resolvers[def.Name] {
			continue
		}
		if best == nil || preferManager(def, best, purlType) {
			best = def
		}
	}
	if best == nil {
		return ""
	}
	return best.Name
}

func preferManager(a, b *definitions.Definition, purlType string) bool {
	if (a.Name == purlType) != (b.Name == purlType) {
		return a.Name == purlType
	}
	if a.Detection.Priority != b.Detection.Priority {
		return a.Detection.Priority < b.Detection.Priority
	}
	return a.Name < b.Name
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
