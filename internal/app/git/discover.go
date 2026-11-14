package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	platformgit "github.com/andrerfcsantos/orbit/internal/platform/git"
	"github.com/andrerfcsantos/orbit/internal/platform/logging"
	"go.uber.org/zap"
)

// Discover finds the repositories the paths name, "." when none are given. A
// path is a repository, a directory holding repositories at any depth beneath
// it, or a directory a repository tracks, which names that repository, as git
// itself would, and limits Clean to that directory, as git clean would. Each
// repository is found once, however many paths lead to it.
//
// Hidden directories and node_modules are not searched, nor ~/Library on macOS:
// repositories are not kept there, and reading them is most of the time a walk
// of a home directory would take.
func (s *Service) Discover(ctx context.Context, paths []string) ([]Repository, error) {
	if len(paths) == 0 {
		paths = []string{"."}
	}
	workDir, home := resolved(s.WorkDir), resolved(s.Home)
	skip := func(dir, name string) bool {
		return strings.HasPrefix(name, ".") || name == "node_modules" ||
			runtime.GOOS == "darwin" && name == "Library" && dir == home
	}

	seen := make(map[string]int)
	var repos []Repository
	for i, path := range paths {
		// The error names the path by its place, not by its value: arguments are
		// not logged.
		which := "the path given"
		if len(paths) > 1 {
			which = fmt.Sprintf("path %d of %d", i+1, len(paths))
		}
		if !filepath.IsAbs(path) && s.WorkDir != "" {
			path = filepath.Join(s.WorkDir, path)
		}
		root, err := filepath.EvalSymlinks(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, fmt.Errorf("%s does not exist", which)
			}
			return nil, fmt.Errorf("%s cannot be read: %w", which, unwrapPath(err))
		}
		if info, err := os.Stat(root); err != nil || !info.IsDir() {
			return nil, fmt.Errorf("%s is not a directory", which)
		}

		found, err := platformgit.Discover(ctx, root, skip)
		if err != nil {
			return nil, err
		}
		within := ""
		if len(found) == 0 {
			if top, prefix, ok := s.enclosing(ctx, root); ok {
				found, within = []string{top}, prefix
			}
		}
		for _, dir := range found {
			at, ok := seen[dir]
			if !ok {
				at = len(repos)
				seen[dir] = at
				repos = append(repos, Repository{Path: dir, Name: name(dir, workDir, home), shared: platformgit.CommonDir(dir)})
				if within != "" {
					repos[at].Within = []string{within}
				}
				continue
			}
			// Named whole by any path, it is worked on whole.
			switch {
			case within == "":
				repos[at].Within = nil
			case repos[at].Within != nil && !slices.Contains(repos[at].Within, within):
				repos[at].Within = append(repos[at].Within, within)
			}
		}
	}
	slices.SortFunc(repos, func(a, b Repository) int { return strings.Compare(a.Path, b.Path) })
	logging.AddFields(ctx, zap.Int("git.paths", len(paths)), zap.Int("git.repositories", len(repos)))
	return repos, nil
}

// enclosing is the repository dir is a directory of, when it tracks a file in
// dir, and dir's path within it. A directory a repository merely contains, untracked
// or ignored -- ~/code in a home directory kept as a dotfiles repository -- is not
// one of its own.
func (s *Service) enclosing(ctx context.Context, dir string) (string, string, bool) {
	out, err := s.Git.Climb(ctx, dir, "rev-parse", "--show-toplevel", "--show-prefix")
	if err != nil {
		return "", "", false
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 2 || lines[1] == "" {
		return "", "", false
	}
	prefix := strings.TrimSuffix(lines[1], "/")
	// The index, not HEAD: a directory added but not yet committed is tracked.
	if _, err := s.Git.Climb(ctx, dir, "ls-files", "--cached", "--error-unmatch", "--", "."); err != nil {
		return "", "", false
	}
	return filepath.Clean(lines[0]), prefix, true
}

// name is how a repository is shown: relative to the directory orbit runs in,
// its own name when it is that directory, from ~, or its whole path.
//
// Names are written with forward slashes everywhere, as git writes paths: a
// backslash in a name is shown quoted, being easy to take for an escape.
func name(path, workDir, home string) string {
	if rel, ok := beneath(path, workDir); ok {
		if rel == "." {
			return filepath.Base(path)
		}
		return filepath.ToSlash(rel)
	}
	if rel, ok := beneath(path, home); ok {
		if rel == "." {
			return "~"
		}
		return "~/" + filepath.ToSlash(rel)
	}
	return filepath.ToSlash(path)
}

func beneath(path, dir string) (string, bool) {
	if dir == "" {
		return "", false
	}
	rel, err := filepath.Rel(dir, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return rel, true
}

func resolved(dir string) string {
	if dir == "" {
		return ""
	}
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		return real
	}
	return dir
}

// unwrapPath drops the path an *os.PathError names.
func unwrapPath(err error) error {
	var path *os.PathError
	if errors.As(err, &path) {
		return path.Err
	}
	return err
}
