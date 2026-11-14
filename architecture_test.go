// Package main's architecture test enforces orbit's layering.
//
// orbit is CLI-only today, but its layers are kept apart so another surface (an
// HTTP API, a UI) can be added later without moving behavior. A use case that
// imports cobra can only be driven from the CLI; one that imports net/http can
// only be driven from an API. Forbidding those imports keeps a tool's behavior
// somewhere every surface can reach.
//
// See docs/architecture.md for the rationale in prose.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const modulePath = "github.com/andrerfcsantos/orbit"

func TestAppLayerIsSurfaceAgnostic(t *testing.T) {
	// internal/app holds the use cases every surface shares. If one of them reaches
	// for a delivery mechanism, that functionality becomes available through that
	// mechanism only -- which is exactly how a tool ends up CLI-only by accident.
	assertNoImports(t, "./internal/app/...",
		[]string{modulePath + "/internal/transport/", charmPrefix},
		[]string{
			"github.com/spf13/cobra",
			"net/http",
		},
	)
}

func TestCoreLayerIsFrameworkFree(t *testing.T) {
	// Domain logic must be testable and reusable without a framework present.
	assertNoImports(t, "./internal/core/...",
		[]string{
			modulePath + "/internal/transport/",
			modulePath + "/internal/cmd/",
			charmPrefix,
		},
		[]string{
			"github.com/spf13/cobra",
			"net/http",
		},
	)
}

// charmPrefix holds the terminal's delivery mechanisms: lipgloss, Bubble Tea and
// Bubbles.
const charmPrefix = "charm.land/"

func TestPlatformDoesNotDependOnTransportOrCommands(t *testing.T) {
	// Platform is infrastructure the layers above build on, so it must not depend on
	// them. net/http is deliberately not banned here: platform/logging supplies the
	// HTTP middleware that produces the canonical entry per request, and that is
	// infrastructure rather than routing.
	assertNoImports(t, "./internal/platform/...",
		[]string{
			modulePath + "/internal/transport/",
			modulePath + "/internal/cmd/",
			modulePath + "/internal/app/",
		},
		nil,
	)
}

func TestTransportDoesNotDependOnCommands(t *testing.T) {
	// Transports adapt app services. A transport reaching into internal/cmd would
	// mean the API depended on the CLI's wiring.
	assertNoImports(t, "./internal/transport/...",
		[]string{modulePath + "/internal/cmd/"},
		nil,
	)
}

func TestEveryPackageHasADocComment(t *testing.T) {
	// A package comment is where a layer's purpose and constraints are recorded. In a
	// skeleton meant to be extended by others, an undocumented package is an
	// invitation to put the wrong thing in it.
	//
	// The package list is discovered rather than hardcoded, so a newly added package
	// is covered automatically -- a fixed list would let exactly the packages most in
	// need of explanation escape the rule.
	output, err := exec.Command("go", "list", "-f", "{{.Dir}} {{.Name}}", "./...").Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			t.Fatalf("go list failed: %v\n%s", err, exitErr.Stderr)
		}
		t.Fatalf("go list failed: %v", err)
	}

	root, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		dir, name, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok || name == "main" && dir == root {
			// The repo root's package is this test file itself.
			continue
		}
		relative, err := filepath.Rel(root, dir)
		if err != nil {
			relative = dir
		}
		if !hasPackageComment(t, dir, name) {
			want := "// Package " + name
			if name == "main" {
				want = "// Command <name>"
			}
			t.Errorf("package %s has no %q doc comment adjacent to its package clause", relative, want)
		}
	}
}

func TestArchitectureDocumentationExists(t *testing.T) {
	for _, name := range []string{"README.md", "docs/architecture.md"} {
		if _, err := os.Stat(name); err != nil {
			t.Errorf("missing %s: %v", name, err)
		}
	}
}

// hasPackageComment reports whether some .go file in dir carries a doc comment
// immediately above its package clause.
//
// The comment must be adjacent to the clause, not merely present in the file: Go
// only treats it as the package's documentation in that position, so a matching
// string elsewhere would satisfy a substring search while `go doc` still showed
// nothing.
func hasPackageComment(t *testing.T, dir, name string) bool {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Errorf("read %s: %v", dir, err)
		return false
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, entry.Name()), nil, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			continue
		}
		if file.Doc == nil {
			continue
		}
		doc := strings.TrimSpace(file.Doc.Text())
		// A command's doc comment conventionally opens with "Command <name>" rather
		// than "Package main", and godoc renders it that way, so both are accepted.
		if name == "main" {
			if strings.HasPrefix(doc, "Command ") || strings.HasPrefix(doc, "Package main") {
				return true
			}
			continue
		}
		if strings.HasPrefix(doc, "Package "+name) {
			return true
		}
	}
	return false
}

// assertNoImports fails when any package matching pattern imports something with
// one of the given prefixes, or one of the exact paths.
func assertNoImports(t *testing.T, pattern string, bannedPrefixes, bannedExact []string) {
	t.Helper()

	var violations []string
	for pkg, deps := range listImports(t, pattern) {
		for _, dep := range deps {
			for _, prefix := range bannedPrefixes {
				if strings.HasPrefix(dep, prefix) {
					violations = append(violations, pkg+" -> "+dep)
				}
			}
			if slices.Contains(bannedExact, dep) {
				violations = append(violations, pkg+" -> "+dep)
			}
		}
	}
	if len(violations) > 0 {
		slices.Sort(violations)
		violations = slices.Compact(violations)
		t.Errorf("forbidden dependencies:\n  %s", strings.Join(violations, "\n  "))
	}
}

// listImports returns the imports of every package matching pattern, including
// those pulled in only by tests: a test that reached across a layer boundary would
// otherwise hide a dependency the rules forbid.
func listImports(t *testing.T, pattern string) map[string][]string {
	t.Helper()

	cmd := exec.Command("go", "list", "-json", pattern)
	output, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			t.Fatalf("go list %s failed: %v\n%s", pattern, err, exitErr.Stderr)
		}
		t.Fatalf("go list %s failed: %v", pattern, err)
	}

	type listed struct {
		ImportPath  string
		Imports     []string
		TestImports []string
	}

	packages := make(map[string][]string)
	decoder := json.NewDecoder(bytes.NewReader(output))
	for {
		var pkg listed
		if err := decoder.Decode(&pkg); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("decode go list output: %v", err)
		}
		packages[pkg.ImportPath] = append(pkg.Imports, pkg.TestImports...)
	}
	return packages
}
