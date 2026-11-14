package git

import (
	"slices"
	"strings"
	"testing"

	core "github.com/andrerfcsantos/orbit/internal/core/git"
)

// A plan of many paths is cleaned in several gits, each command line short
// enough for Windows, and every path in exactly one.
func TestBatchesFitTheCommandLine(t *testing.T) {
	var paths []string
	for i := range 2000 {
		paths = append(paths, strings.Repeat("x", 90)+string(rune('a'+i%26))+"/"+strings.Repeat("y", i%7))
	}
	groups := batches(paths)
	if len(groups) < 2 {
		t.Fatalf("%d groups", len(groups))
	}
	var all []string
	for _, group := range groups {
		size := 0
		for _, path := range group {
			size += len(core.Literal(path)) + 1
		}
		if size > maxArgumentBytes {
			t.Errorf("a group of %d bytes", size)
		}
		all = append(all, group...)
	}
	if !slices.Equal(all, paths) {
		t.Error("the groups are not the paths, in order")
	}
	if groups := batches(nil); len(groups) != 1 || groups[0] != nil {
		t.Errorf("no paths: %q", groups)
	}
}
