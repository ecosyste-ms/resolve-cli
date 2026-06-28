package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// These tests compile and run the resolve binary. They hit the network
// (registry metadata) and, for the full-resolve cases, require the relevant
// package manager on PATH. Skipped under -short.

var testBinary string

func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
	dir, err := os.MkdirTemp("", "resolve-cli-test-*")
	if err != nil {
		return 1
	}
	defer func() { _ = os.RemoveAll(dir) }()

	testBinary = filepath.Join(dir, "resolve")
	cmd := exec.Command("go", "build", "-o", testBinary, ".")
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return 1
	}

	return m.Run()
}

func runResolve(t *testing.T, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	cmd := exec.Command(testBinary, args...)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	exitCode = 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		exitCode = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("running %v: %v", args, err)
	}
	return outBuf.String(), errBuf.String(), exitCode
}

func haveBinary(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// TestRegistryFlagRecognised checks that the registry names used by
// resolve.ecosyste.ms (bare hostnames synced from packages.ecosyste.ms) are
// accepted. We don't assert which ecosystem each maps to; only that the
// mapping chain produces something usable. A failure here means the service
// would return "could not determine ecosystem" for that registry.
func TestRegistryFlagRecognised(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in -short mode")
	}

	registries := []string{
		"packagist.org",
		"rubygems.org",
		"npmjs.org",
		"crates.io",
		"pypi.org",
		"proxy.golang.org",
		"repo1.maven.org",
		"hex.pm",
		"nuget.org",
		"pub.dev",
		"anaconda.org",
		"clojars.org",
	}

	for _, reg := range registries {
		t.Run(reg, func(t *testing.T) {
			_, stderr, _ := runResolve(t, "--registry", reg, "--package", "does-not-exist", "--timeout", "20")
			if strings.Contains(stderr, "could not determine ecosystem") {
				t.Errorf("registry %q not recognised: %s", reg, strings.TrimSpace(stderr))
			}
			if strings.Contains(stderr, "no resolver available") {
				t.Errorf("no manager selected for %q: %s", reg, strings.TrimSpace(stderr))
			}
		})
	}
}

// TestEcosystemFlagRecognised checks that --ecosystem accepts both purl types
// and ecosystem names.
func TestEcosystemFlagRecognised(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in -short mode")
	}

	for _, eco := range []string{"composer", "packagist", "gem", "rubygems", "npm", "go", "golang", "pypi"} {
		t.Run(eco, func(t *testing.T) {
			_, stderr, _ := runResolve(t, "--ecosystem", eco, "--package", "does-not-exist", "--timeout", "20")
			if strings.Contains(stderr, "no resolver available") {
				t.Errorf("ecosystem %q not recognised: %s", eco, strings.TrimSpace(stderr))
			}
		})
	}
}

// TestResolvePackagist runs a full resolve against packagist.org and checks
// the output for the issues reported in ecosyste-ms/resolve#842. Requires
// composer on PATH.
func TestResolvePackagist(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in -short mode")
	}
	if !haveBinary("composer") {
		t.Skip("composer not installed")
	}

	stdout, stderr, code := runResolve(t, "--registry", "packagist.org", "--package", "google/auth")
	if code != 0 {
		t.Fatalf("resolve failed: %s", strings.TrimSpace(stderr))
	}

	var results map[string]string
	if err := json.Unmarshal([]byte(stdout), &results); err != nil {
		t.Fatalf("invalid JSON output: %v\n%s", err, stdout)
	}
	if len(results) == 0 {
		t.Fatal("expected at least one resolved dependency")
	}

	for name, version := range results {
		if strings.Contains(name, "--") {
			t.Errorf("tree-drawing prefix in package name: %q", name)
		}
		if name == "php" || strings.HasPrefix(name, "ext-") || strings.HasPrefix(name, "lib-") {
			t.Errorf("platform package leaked into results: %q", name)
		}
		if strings.ContainsAny(version, "^~*|<>= ") {
			t.Errorf("constraint instead of resolved version for %s: %q", name, version)
		}
	}
}

// TestResolveRubygems runs a full resolve against rubygems.org for a small gem.
// Requires bundler on PATH.
func TestResolveRubygems(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in -short mode")
	}
	if !haveBinary("bundle") {
		t.Skip("bundler not installed")
	}

	stdout, stderr, code := runResolve(t, "--registry", "rubygems.org", "--package", "rack")
	if code != 0 {
		t.Fatalf("resolve failed: %s", strings.TrimSpace(stderr))
	}

	var results map[string]string
	if err := json.Unmarshal([]byte(stdout), &results); err != nil {
		t.Fatalf("invalid JSON output: %v\n%s", err, stdout)
	}
}
