package git

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	core "github.com/andrerfcsantos/orbit/internal/core/git"
	"github.com/andrerfcsantos/orbit/internal/platform/logging"
	"go.uber.org/zap"
)

// GCOptions are how GC collects.
type GCOptions struct {
	// Aggressive repacks everything anew, as git gc --aggressive: much slower, for
	// a somewhat smaller repository.
	Aggressive bool
}

// Collected is how much a repository's objects took before and after collecting
// its garbage.
type Collected struct {
	BytesBefore int64 `json:"bytes_before"`
	BytesAfter  int64 `json:"bytes_after"`
}

// Freed is how many bytes collecting freed.
func (c Collected) Freed() int64 { return max(0, c.BytesBefore-c.BytesAfter) }

// gcParallel is how many repositories are collected at once: git gc already
// repacks on every core, so more would only queue them.
func gcParallel() int { return max(2, runtime.NumCPU()/2) }

// GC runs git gc in every repository.
func (s *Service) GC(ctx context.Context, repos []Repository, options GCOptions, progress Progress[Collected]) []Result[Collected] {
	logging.AddFields(ctx, zap.Bool("git.gc.aggressive", options.Aggressive))
	results := each(ctx, s, repos, gcParallel(), progress, func(t *task) (Collected, bool, error) {
		collected, err := t.gc(options.Aggressive)
		return collected, collected.Freed() > 0, err
	})
	var freed int64
	for _, result := range results {
		freed += result.Detail.Freed()
	}
	logging.AddFields(ctx, zap.Int64("git.gc.freed_bytes", freed))
	record(ctx, "gc", results, "freed space in", "with nothing to free")
	return results
}

func (t *task) gc(aggressive bool) (Collected, error) {
	var collected Collected
	var err error
	t.step("measuring")
	if collected.BytesBefore, err = t.objectBytes(); err != nil {
		return collected, err
	}
	// Nothing is freed until a measurement after says so: a gc that failed, or
	// could not be measured, freed nothing to report.
	collected.BytesAfter = collected.BytesBefore
	t.step("collecting garbage")
	args := []string{"gc", "--quiet"}
	if aggressive {
		args = append(args, "--aggressive")
	}
	if _, err := t.run(args...); err != nil {
		return collected, err
	}
	after, err := t.objectBytes()
	if err != nil {
		return collected, err
	}
	collected.BytesAfter = after
	return collected, nil
}

// Cleaned is what cleaning removed from a repository, or would.
type Cleaned struct {
	// Paths are the untracked and ignored paths removed; a directory ends in "/".
	Paths     []string  `json:"paths"`
	Collected Collected `json:"collected"`
}

// Uncleaned finds what Clean would remove from every repository, removing nothing.
func (s *Service) Uncleaned(ctx context.Context, repos []Repository, progress Progress[Cleaned]) []Result[Cleaned] {
	results := each(ctx, s, repos, 32, progress, func(t *task) (Cleaned, bool, error) {
		t.step("finding untracked and ignored files")
		out, err := t.run(clean("-ffxdn", t.repo.Within)...)
		cleaned := Cleaned{Paths: core.Cleaned(out)}
		return cleaned, len(cleaned.Paths) > 0, err
	})
	record(ctx, "clean-plan", results, "found files to remove in", "with none")
	return results
}

// clean is git clean with flags, limited to paths, each named literally, or in
// all of the repository when there are none. Paths are printed as they are,
// as far as git allows, so they can be named back.
func clean(flags string, paths []string) []string {
	args := []string{"-c", "core.quotePath=false", "clean", flags}
	if len(paths) > 0 {
		args = append(args, "--")
		for _, path := range paths {
			args = append(args, core.Literal(path))
		}
	}
	return args
}

// Clean removes every untracked file from every repository, ignored ones
// included -- build output, dependencies, .env files, editor settings -- and the
// repositories nested in them, as git clean -ffxd does, and then collects its
// garbage.
func (s *Service) Clean(ctx context.Context, repos []Repository, progress Progress[Cleaned]) []Result[Cleaned] {
	results := each(ctx, s, repos, gcParallel(), progress, func(t *task) (Cleaned, bool, error) {
		return t.clean(t.repo.Within, false)
	})
	recordClean(ctx, results)
	return results
}

// CleanPlanned removes what Uncleaned found, and nothing else: a file that
// appeared since is left alone, unless it is beneath a directory the plan
// listed. A repository whose plan failed, or was stopped, is not cleaned, and its
// plan is returned as its result.
func (s *Service) CleanPlanned(ctx context.Context, plans []Result[Cleaned], progress Progress[Cleaned]) []Result[Cleaned] {
	var repos []Repository
	planned := make(map[string][]string)
	for _, plan := range plans {
		if plan.Outcome == OutcomeChanged || plan.Outcome == OutcomeUnchanged {
			repos = append(repos, plan.Repository)
			planned[plan.Repository.Path] = plan.Detail.Paths
		}
	}
	results := alongside(plans, each(ctx, s, repos, gcParallel(), progress, func(t *task) (Cleaned, bool, error) {
		return t.clean(t.stillFiles(planned[t.repo.Path]), true)
	}))
	recordClean(ctx, results)
	return results
}

// stillFiles is the planned paths less any listed as a file that is a directory
// now: git clean would remove all of it, which nobody was shown.
func (t *task) stillFiles(paths []string) []string {
	kept := make([]string, 0, len(paths))
	for _, path := range paths {
		if !strings.HasSuffix(path, "/") {
			if info, err := os.Lstat(filepath.Join(t.repo.Path, filepath.FromSlash(path))); err == nil && info.IsDir() {
				t.note("%s was a file when it was listed and is a directory now, so it was left alone", path)
				continue
			}
		}
		kept = append(kept, path)
	}
	return kept
}

// clean removes the paths -- all that is untracked when there are none, unless
// only those were asked for -- and then collects garbage.
func (t *task) clean(paths []string, only bool) (Cleaned, bool, error) {
	var cleaned Cleaned
	if len(paths) > 0 || !only {
		t.step("removing untracked and ignored files")
		for _, batch := range batches(paths) {
			out, err := t.run(clean("-ffxd", batch)...)
			cleaned.Paths = append(cleaned.Paths, core.Cleaned(out)...)
			if err != nil {
				return cleaned, len(cleaned.Paths) > 0, err
			}
		}
	}
	var err error
	cleaned.Collected, err = t.gc(false)
	return cleaned, len(cleaned.Paths) > 0 || cleaned.Collected.Freed() > 0, err
}

// batches splits paths into groups small enough for one command line; no
// paths is one group of none.
func batches(paths []string) [][]string {
	if len(paths) == 0 {
		return [][]string{nil}
	}
	return chunks(paths, func(path string) int { return len(core.Literal(path)) + 1 })
}

// maxArgumentBytes bounds what one git is given on its command line. Windows
// allows 32,767 characters, and quoting an argument can double it.
const maxArgumentBytes = 12 << 10

// chunks splits items, in order, into groups whose arguments, as cost counts
// them, fit one command line.
func chunks[T any](items []T, cost func(T) int) [][]T {
	var groups [][]T
	start, size := 0, 0
	for i, item := range items {
		if size+cost(item) > maxArgumentBytes && i > start {
			groups = append(groups, items[start:i])
			start, size = i, 0
		}
		size += cost(item)
	}
	if start < len(items) {
		groups = append(groups, items[start:])
	}
	return groups
}

func recordClean(ctx context.Context, results []Result[Cleaned]) {
	paths := 0
	for _, result := range results {
		paths += len(result.Detail.Paths)
	}
	logging.AddFields(ctx, zap.Int("git.clean.removed_paths", paths))
	record(ctx, "clean", results, "cleaned", "already clean")
}
