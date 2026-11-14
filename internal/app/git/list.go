package git

import (
	"context"

	core "github.com/andrerfcsantos/orbit/internal/core/git"
)

// Status is where a repository stands, as it is on disk: nothing is fetched.
type Status struct {
	// Branch is the branch checked out, empty when HEAD is detached.
	Branch   string `json:"branch,omitempty"`
	Detached bool   `json:"detached,omitempty"`
	// Default is the default branch origin's HEAD names, as last fetched; empty
	// when it was never recorded.
	Default string `json:"default_branch,omitempty"`
	// Commit is HEAD's, empty before the first.
	Commit   string `json:"commit,omitempty"`
	Upstream string `json:"upstream,omitempty"`
	// UpstreamGone is an upstream branch that no longer exists on its remote.
	UpstreamGone bool `json:"upstream_gone,omitempty"`
	Ahead        int  `json:"ahead"`
	Behind       int  `json:"behind"`
	// Changes counts the paths with changes, untracked ones included.
	Changes int `json:"changes"`
	Stashes int `json:"stashes"`
}

// List reads where every repository stands.
func (s *Service) List(ctx context.Context, repos []Repository, progress Progress[Status]) []Result[Status] {
	results := each(ctx, s, repos, 32, progress, func(t *task) (Status, bool, error) {
		t.step("reading its status")
		status, err := t.status()
		if err != nil {
			return Status{}, false, err
		}
		stashes, err := t.run("stash", "list", "--format=%gd")
		if err != nil {
			return Status{}, false, err
		}
		head, _ := t.remoteHead("origin")
		return Status{
			Branch:       status.Branch,
			Detached:     status.Branch == "" && status.Commit != "",
			Default:      head,
			Commit:       status.Commit,
			Upstream:     status.Upstream,
			UpstreamGone: status.Upstream != "" && !status.Compared,
			Ahead:        status.Ahead,
			Behind:       status.Behind,
			Changes:      len(status.Paths),
			Stashes:      len(core.Lines(stashes)),
		}, false, nil
	})
	record(ctx, "list", results, "listed", "listed")
	return results
}
