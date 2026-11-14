// Package git is orbit's batch git: it finds the repositories beneath the
// directories it is given and, in every one of them at once, lists their
// branches, updates them, deletes their old branches, cleans them and collects
// their garbage.
//
// Each repository's work runs in a goroutine of its own, as many at a time as
// the operation suits or Service.Concurrency allows, and reports each step as it
// goes, so a surface can show every repository's progress and the whole batch's.
package git

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/andrerfcsantos/orbit/internal/platform/logging"
	"go.uber.org/zap"
)

// Git runs git in a repository, as platform/git's Runner does.
type Git interface {
	// Run runs git in a repository's top directory, and never above it.
	Run(ctx context.Context, dir string, args ...string) (string, error)
	// Climb runs git in a directory, letting it find the repository the
	// directory is inside.
	Climb(ctx context.Context, dir string, args ...string) (string, error)
}

// Service runs git operations across repositories.
type Service struct {
	Git Git
	// Concurrency bounds how many repositories are worked on at once. Zero picks
	// what suits the operation: more for one waiting on the network, fewer for
	// one using every core, as git gc does.
	Concurrency int
	// Timeout bounds each repository's work, zero for none.
	Timeout time.Duration
	// WorkDir and Home are where repositories are named from: relative to WorkDir
	// beneath it, from ~ beneath Home, by their full path otherwise.
	WorkDir, Home string
	Now           func() time.Time
}

// Repository is a working tree found by Discover.
type Repository struct {
	Path string `json:"path"`
	// Name is the path as a person would recognise it: relative to where orbit
	// was run, or from ~.
	Name string `json:"name"`
	// Within are the directories of the repository it was named by, relative to
	// Path, when it was named by those alone: cleaning it cleans them alone, as
	// git clean run in each would. Empty is the whole repository.
	Within []string `json:"within,omitempty"`

	// shared is the git directory the working tree shares with its other
	// worktrees, whose stash and refs are one.
	shared string
}

// Outcome is how one repository's work ended.
type Outcome string

const (
	// OutcomeChanged is work that changed the repository: pulled, switched,
	// deleted, removed or collected something.
	OutcomeChanged Outcome = "changed"
	// OutcomeUnchanged is work that found nothing to do.
	OutcomeUnchanged Outcome = "unchanged"
	// OutcomeSkipped is work left undone on purpose, for the Reason given.
	OutcomeSkipped Outcome = "skipped"
	// OutcomeFailed is work that went wrong, as Error says.
	OutcomeFailed Outcome = "failed"
	// OutcomeCancelled is work stopped, or never started, because the batch was.
	OutcomeCancelled Outcome = "cancelled"
)

// Result is what an operation did in one repository.
type Result[T any] struct {
	Repository Repository `json:"repository"`
	Outcome    Outcome    `json:"outcome"`
	// Detail is the operation's own account of what it did.
	Detail T `json:"detail"`
	// Reason says why the work was skipped or cancelled.
	Reason string `json:"reason,omitempty"`
	Error  string `json:"error,omitempty"`
	// Notes are what the person should know about, such as changes left in a stash.
	Notes      []string `json:"notes,omitempty"`
	DurationMS int64    `json:"duration_ms"`
}

// EventKind says what an Event reports.
type EventKind int

const (
	// EventStarted is a repository's work beginning.
	EventStarted EventKind = iota
	// EventStep is its work moving on to Step.
	EventStep
	// EventFinished is its work done, with its Result.
	EventFinished
)

// Event reports one repository's progress. Events of different repositories
// arrive from different goroutines, at once.
type Event[T any] struct {
	Kind       EventKind
	Repository Repository
	// Step is what the work is doing now, as a person would say it: "fetching".
	Step   string
	Result *Result[T]
}

// Progress receives events. It must be safe to call from many goroutines.
type Progress[T any] func(Event[T])

// Tally counts results by outcome.
type Tally struct {
	Changed, Unchanged, Skipped, Failed, Cancelled int
}

// Count tallies results.
func Count[T any](results []Result[T]) Tally {
	var tally Tally
	for _, result := range results {
		switch result.Outcome {
		case OutcomeChanged:
			tally.Changed++
		case OutcomeUnchanged:
			tally.Unchanged++
		case OutcomeSkipped:
			tally.Skipped++
		case OutcomeFailed:
			tally.Failed++
		case OutcomeCancelled:
			tally.Cancelled++
		}
	}
	return tally
}

func (r Repository) group() string {
	if r.shared != "" {
		return r.shared
	}
	return r.Path
}

// skipped is work left undone on purpose.
type skipped struct{ reason string }

func (s skipped) Error() string { return s.reason }

func skip(format string, args ...any) error { return skipped{fmt.Sprintf(format, args...)} }

// task is one repository's work.
type task struct {
	ctx   context.Context
	git   Git
	repo  Repository
	notes []string

	mu       sync.Mutex
	current  string
	progress func(string)
}

// run runs git in the repository.
func (t *task) run(args ...string) (string, error) {
	return t.git.Run(t.ctx, t.repo.Path, args...)
}

// step reports what the work is doing now.
func (t *task) step(step string) {
	t.mu.Lock()
	t.current = step
	t.mu.Unlock()
	t.progress(step)
}

func (t *task) note(format string, args ...any) {
	t.notes = append(t.notes, fmt.Sprintf(format, args...))
}

func (t *task) doing() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.current
}

// work is one repository's part of an operation. It reports whether it changed
// the repository, or an error, which skip makes a reason to leave it alone.
type work[T any] func(t *task) (T, bool, error)

// each runs work in every repository, each in a goroutine of its own, at most
// parallel (or Service.Concurrency) at once, and returns the results in the
// order of repos. Worktrees of one repository are worked on one after the
// other: they share a stash, which an update must find as it left it, and refs,
// which two gits would fight over the locks of. A batch cancelled leaves the
// repositories it had not reached cancelled, and stops those it had.
func each[T any](ctx context.Context, s *Service, repos []Repository, parallel int, progress Progress[T], do work[T]) []Result[T] {
	if s.Concurrency > 0 {
		parallel = s.Concurrency
	}
	parallel = max(1, min(parallel, len(repos)))
	if progress == nil {
		progress = func(Event[T]) {}
	}
	logging.AddFields(ctx, zap.Int("git.concurrency", parallel))

	results := make([]Result[T], len(repos))
	slots := make(chan struct{}, parallel)
	groups := make(map[string]chan struct{})
	for _, repo := range repos {
		if groups[repo.group()] == nil {
			groups[repo.group()] = make(chan struct{}, 1)
		}
	}
	var wg sync.WaitGroup
	for i, repo := range repos {
		wg.Go(func() {
			result := &results[i]
			result.Repository = repo
			defer func() { progress(Event[T]{Kind: EventFinished, Repository: repo, Result: result}) }()

			// The group first, so no slot is held waiting for it.
			for _, lock := range []chan struct{}{groups[repo.group()], slots} {
				select {
				case lock <- struct{}{}:
					defer func() { <-lock }()
				case <-ctx.Done():
				}
				if ctx.Err() != nil {
					break
				}
			}
			if ctx.Err() != nil {
				result.Outcome, result.Reason = OutcomeCancelled, "stopped before it started"
				return
			}

			started := time.Now()
			progress(Event[T]{Kind: EventStarted, Repository: repo})
			taskCtx, cancel := ctx, context.CancelFunc(func() {})
			if s.Timeout > 0 {
				taskCtx, cancel = context.WithTimeout(ctx, s.Timeout)
			}
			defer cancel()
			t := &task{ctx: taskCtx, git: s.Git, repo: repo, progress: func(step string) {
				progress(Event[T]{Kind: EventStep, Repository: repo, Step: step})
			}}

			detail, changed, err := do(t)
			result.Detail, result.Notes = detail, t.notes
			result.DurationMS = time.Since(started).Milliseconds()
			// A stop or a timeout comes before anything but a change made: the git
			// it cut short may read as a reason to skip, such as an upstream that
			// seems gone, or as nothing to report, where its error was not one to
			// fail on, as list's default branch is not.
			var reason skipped
			switch {
			case err == nil && changed:
				result.Outcome = OutcomeChanged
			case ctx.Err() != nil:
				result.Outcome, result.Reason = OutcomeCancelled, "stopped while "+t.doing()
			case errors.Is(taskCtx.Err(), context.DeadlineExceeded):
				result.Outcome = OutcomeFailed
				result.Error = fmt.Sprintf("timed out after %s, while %s", s.Timeout, t.doing())
			case err == nil:
				result.Outcome = OutcomeUnchanged
			case errors.As(err, &reason):
				result.Outcome, result.Reason = OutcomeSkipped, reason.reason
			default:
				result.Outcome, result.Error = OutcomeFailed, err.Error()
			}
		})
	}
	wg.Wait()
	return results
}

// alongside is a result for every plan, in the plans' order: what was done for
// it, with what planning noted first -- such as remote branches left alone --
// or the plan itself where there was nothing to do, which says why -- none
// found, skipped, failed -- so that every repository planned is accounted for.
func alongside[T any](plans, results []Result[T]) []Result[T] {
	done := make(map[string]Result[T], len(results))
	for _, result := range results {
		done[result.Repository.Path] = result
	}
	merged := make([]Result[T], len(plans))
	for i, plan := range plans {
		merged[i] = plan
		if result, ok := done[plan.Repository.Path]; ok {
			result.Notes = append(slices.Clone(plan.Notes), result.Notes...)
			merged[i] = result
		}
	}
	return merged
}

// record puts what an operation did on the invocation's canonical entry.
func record[T any](ctx context.Context, operation string, results []Result[T], changed, unchanged string) {
	tally := Count(results)
	logging.AddFields(ctx,
		zap.String("git.operation", operation),
		zap.Int("git.repositories", len(results)),
		zap.Int("git.changed", tally.Changed),
		zap.Int("git.unchanged", tally.Unchanged),
		zap.Int("git.skipped", tally.Skipped),
		zap.Int("git.failed", tally.Failed),
		zap.Int("git.cancelled", tally.Cancelled),
	)
	summary := fmt.Sprintf("%s %d of %d %s", changed, tally.Changed, len(results), plural(len(results), "repository", "repositories"))
	var rest []string
	for _, part := range []struct {
		count int
		label string
	}{{tally.Unchanged, unchanged}, {tally.Skipped, "skipped"}, {tally.Failed, "failed"}, {tally.Cancelled, "cancelled"}} {
		if part.count > 0 {
			rest = append(rest, fmt.Sprintf("%d %s", part.count, part.label))
		}
	}
	if len(rest) > 0 {
		summary += ", " + strings.Join(rest, ", ")
	}
	logging.Summarize(ctx, summary)
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func plural(count int, one, many string) string {
	if count == 1 {
		return one
	}
	return many
}
