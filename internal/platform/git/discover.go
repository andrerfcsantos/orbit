package git

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// IsRepository reports whether dir is the top of a git working tree: it holds
// .git, as a directory, or as the file a linked worktree or submodule has.
func IsRepository(dir string) bool {
	_, err := os.Lstat(filepath.Join(dir, ".git"))
	return err == nil
}

// CommonDir is the git directory a working tree shares with its other
// worktrees, read from disk alone: its .git directory, or for a linked worktree
// the directory its .git file names, less the part that is the worktree's own.
// Worktrees of one repository share its refs and its stash. It is dir/.git when
// nothing better can be read.
func CommonDir(dir string) string {
	dotGit := filepath.Join(dir, ".git")
	info, err := os.Stat(dotGit)
	if err != nil {
		return dotGit
	}
	if info.IsDir() {
		if resolved, err := filepath.EvalSymlinks(dotGit); err == nil {
			return resolved
		}
		return dotGit
	}
	data, err := os.ReadFile(dotGit)
	if err != nil {
		return dotGit
	}
	gitDir, ok := strings.CutPrefix(line(data), "gitdir: ")
	if !ok {
		return dotGit
	}
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(dir, gitDir)
	}
	common := gitDir
	if data, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
		common = line(data)
		if !filepath.IsAbs(common) {
			common = filepath.Join(gitDir, common)
		}
	}
	if resolved, err := filepath.EvalSymlinks(common); err == nil {
		return resolved
	}
	return filepath.Clean(common)
}

// line is what a one-line file of git's holds, less its line ending only: a
// path's trailing space is part of the path.
func line(data []byte) string {
	return strings.TrimSuffix(strings.TrimSuffix(string(data), "\n"), "\r")
}

// discoverWorkers is how many directories are read at once.
const discoverWorkers = 32

// Discover returns the repositories beneath root, root itself when it is one,
// in path order. A repository is a leaf: what is beneath it is its own, so it is
// not searched. Symbolic links are not followed, and a directory skip says to
// leave is not entered, nor anything beneath it. A directory that cannot be read
// is passed over.
func Discover(ctx context.Context, root string, skip func(dir string, name string) bool) ([]string, error) {
	w := &walker{ctx: ctx, skip: skip, queue: []string{root}, pending: 1}
	w.wake = sync.NewCond(&w.mu)
	stop := context.AfterFunc(ctx, func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		w.wake.Broadcast()
	})
	defer stop()

	var workers sync.WaitGroup
	for range discoverWorkers {
		workers.Go(w.work)
	}
	workers.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	slices.Sort(w.found)
	return w.found, nil
}

type walker struct {
	ctx  context.Context
	skip func(dir, name string) bool

	mu   sync.Mutex
	wake *sync.Cond
	// queue holds the directories waiting to be read; pending counts those and
	// the ones being read, so the walk is over when it reaches zero.
	queue   []string
	pending int
	found   []string
}

func (w *walker) work() {
	for {
		w.mu.Lock()
		for len(w.queue) == 0 && w.pending > 0 && w.ctx.Err() == nil {
			w.wake.Wait()
		}
		if len(w.queue) == 0 || w.ctx.Err() != nil {
			w.mu.Unlock()
			return
		}
		dir := w.queue[len(w.queue)-1]
		w.queue = w.queue[:len(w.queue)-1]
		w.mu.Unlock()

		repository, children := w.visit(dir)

		w.mu.Lock()
		if repository {
			w.found = append(w.found, dir)
		}
		w.queue = append(w.queue, children...)
		w.pending += len(children) - 1
		if len(children) > 0 || w.pending == 0 {
			w.wake.Broadcast()
		}
		w.mu.Unlock()
	}
}

func (w *walker) visit(dir string) (bool, []string) {
	if IsRepository(dir) {
		return true, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, nil
	}
	var children []string
	for _, entry := range entries {
		if entry.IsDir() && (w.skip == nil || !w.skip(dir, entry.Name())) {
			children = append(children, filepath.Join(dir, entry.Name()))
		}
	}
	return false, children
}
