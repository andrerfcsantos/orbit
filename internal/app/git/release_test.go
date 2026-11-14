package git

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	core "github.com/andrerfcsantos/orbit/internal/core/git"
)

func inspect(t *testing.T, s *Service, dir string, options ReleaseOptions) *ReleaseState {
	t.Helper()
	repo, err := s.LocateRelease(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	st, err := s.InspectRelease(context.Background(), repo, options)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestAReleaseTagsTheRemotesBranchFollowingItsConvention(t *testing.T) {
	root := isolate(t)
	bare, upstream := remote(t, root)
	run(t, upstream, nil, "tag", "release-1.2.0")
	run(t, upstream, nil, "push", "--quiet", "origin", "release-1.2.0")
	local := clone(t, bare, filepath.Join(root, "local"))
	commit(t, upstream, "c.txt", "c\n", time.Time{})
	run(t, upstream, nil, "push", "--quiet")
	head := run(t, upstream, nil, "rev-parse", "HEAD")
	commit(t, local, "mine.txt", "not pushed\n", time.Time{})

	s := service(root)
	st := inspect(t, s, filepath.Join(local), ReleaseOptions{Push: true})
	if st.Commit != head || st.Branch != "main" || st.Remote != "origin" || st.Prefix != "release-" || st.Unpushed != 1 {
		t.Fatalf("state = %+v", st)
	}
	plan, err := st.Plan(core.Choice{})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Tag != "release-1.3.0" || plan.Previous != "release-1.2.0" || len(plan.Warnings) != 1 {
		t.Fatalf("plan = %+v", plan)
	}
	release, err := s.CreateRelease(context.Background(), st, plan, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if !release.Created || !release.Pushed {
		t.Errorf("release = %+v", release)
	}
	if got := run(t, bare, nil, "rev-parse", "release-1.3.0^{commit}"); got != head {
		t.Errorf("tagged %s, want %s", got, head)
	}
	if kind := run(t, bare, nil, "cat-file", "-t", "release-1.3.0"); kind != "tag" {
		t.Errorf("the tag is a %s, not annotated", kind)
	}
	if message := run(t, bare, nil, "tag", "-l", "--format=%(contents:subject)", "release-1.3.0"); message != "Release release-1.3.0" {
		t.Errorf("message = %q", message)
	}

	// Read again, the commit is released, and a second release of it refused.
	if _, err := inspect(t, s, local, ReleaseOptions{Push: true}).Plan(core.Choice{Bump: core.BumpPatch}); err == nil ||
		!strings.Contains(err.Error(), "released already") {
		t.Errorf("err = %v", err)
	}
}

func TestATagNeverPushedIsNoRelease(t *testing.T) {
	root := isolate(t)
	bare, upstream := remote(t, root)
	run(t, upstream, nil, "tag", "v1.0.0", "HEAD~1")
	run(t, upstream, nil, "push", "--quiet", "origin", "v1.0.0")
	local := clone(t, bare, filepath.Join(root, "local"))
	run(t, local, nil, "tag", "v1.1.0", "HEAD~1")

	st := inspect(t, service(root), local, ReleaseOptions{Push: true})
	if _, err := st.Plan(core.Choice{}); err == nil || !strings.Contains(err.Error(), "exists already") {
		t.Errorf("err = %v", err)
	}
	if plan, err := st.Plan(core.Choice{Bump: core.BumpPatch}); err != nil || plan.Tag != "v1.0.1" {
		t.Errorf("plan = %+v, err = %v", plan, err)
	}
	// Tagged here only, it is a release.
	if plan, err := inspect(t, service(root), local, ReleaseOptions{}).Plan(core.Choice{}); err != nil || plan.Tag != "v1.2.0" {
		t.Errorf("plan = %+v, err = %v", plan, err)
	}
}

func TestATagWhosePushFailedIsDeletedAgain(t *testing.T) {
	root := isolate(t)
	bare, _ := remote(t, root)
	local := clone(t, bare, filepath.Join(root, "local"))
	hook := filepath.Join(bare, "hooks", "pre-receive")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\necho no tags today >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	s := service(root)
	st := inspect(t, s, local, ReleaseOptions{Push: true})
	plan, err := st.Plan(core.Choice{})
	if err != nil {
		t.Fatal(err)
	}
	// A message git would clean up, were it not kept verbatim.
	release, err := s.CreateRelease(context.Background(), st, plan, "Release\n\n\nDetails  \n# not a comment", true)
	if err == nil || !strings.Contains(err.Error(), "deleted again") || release.Created || release.Pushed {
		t.Fatalf("release = %+v, err = %v", release, err)
	}
	if tags := run(t, local, nil, "tag", "--list"); tags != "" {
		t.Errorf("tags left: %s", tags)
	}
}

func TestAReleaseOfADetachedHeadNeedsItsBranch(t *testing.T) {
	root := isolate(t)
	bare, _ := remote(t, root)
	local := clone(t, bare, filepath.Join(root, "local"))
	run(t, local, nil, "checkout", "--quiet", "--detach", "HEAD~1")

	s := service(root)
	repo, err := s.LocateRelease(context.Background(), local)
	if err != nil {
		t.Fatal(err)
	}
	if repo.Branch != "" || s.DefaultBranch(context.Background(), repo.Repository) != "main" {
		t.Errorf("repo = %+v", repo)
	}
	if _, err := s.InspectRelease(context.Background(), repo, ReleaseOptions{Push: true}); err == nil {
		t.Error("released a detached HEAD")
	}
	st, err := s.InspectRelease(context.Background(), repo, ReleaseOptions{Branch: "main", Push: true})
	if err != nil {
		t.Fatal(err)
	}
	if st.Commit != run(t, local, nil, "rev-parse", "origin/main") {
		t.Errorf("commit = %s", st.Commit)
	}
	if _, err := s.InspectRelease(context.Background(), repo, ReleaseOptions{Branch: "nope", Push: true}); err == nil ||
		!strings.Contains(err.Error(), "push it first") {
		t.Errorf("err = %v", err)
	}
}

func TestATagIsDeletedAgainWhenItsPushIsStopped(t *testing.T) {
	root := isolate(t)
	bare, _ := remote(t, root)
	local := clone(t, bare, filepath.Join(root, "local"))
	hook := filepath.Join(bare, "hooks", "pre-receive")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	s := service(root)
	st := inspect(t, s, local, ReleaseOptions{Push: true})
	plan, err := st.Plan(core.Choice{})
	if err != nil {
		t.Fatal(err)
	}
	s.Timeout = 2 * time.Second
	if _, err := s.CreateRelease(context.Background(), st, plan, "", true); err == nil || !strings.Contains(err.Error(), "deleted again") {
		t.Fatalf("err = %v", err)
	}
	if tags := run(t, local, nil, "tag", "--list"); tags != "" {
		t.Errorf("tags left: %s", tags)
	}
}

func TestRecheckRefusesAPlanTheRepositoryMovedOnFrom(t *testing.T) {
	root := isolate(t)
	bare, upstream := remote(t, root)
	run(t, upstream, nil, "tag", "v1.0.0", "HEAD~1")
	run(t, upstream, nil, "push", "--quiet", "origin", "v1.0.0")
	local := clone(t, bare, filepath.Join(root, "local"))

	s := service(root)
	st := inspect(t, s, local, ReleaseOptions{Push: true})
	plan, err := st.Plan(core.Choice{Stage: "beta"})
	if err != nil || plan.Tag != "v1.1.0-beta.1" {
		t.Fatalf("plan = %+v, err = %v", plan, err)
	}
	if _, err := s.Recheck(context.Background(), st, plan); err != nil {
		t.Fatalf("nothing moved, and: %v", err)
	}
	// Somebody releases 1.1.0 while the question is open.
	run(t, upstream, nil, "tag", "v1.1.0")
	run(t, upstream, nil, "push", "--quiet", "origin", "v1.1.0")
	if _, err := s.Recheck(context.Background(), st, plan); err == nil || !strings.Contains(err.Error(), "changed while you were asked") {
		t.Errorf("err = %v", err)
	}
}

func TestAShallowCloneIsRefused(t *testing.T) {
	root := isolate(t)
	bare, upstream := remote(t, root)
	run(t, upstream, nil, "tag", "v1.4.0", "HEAD~1")
	run(t, upstream, nil, "push", "--quiet", "origin", "v1.4.0")
	local := filepath.Join(root, "shallow")
	run(t, root, nil, "clone", "--quiet", "--depth", "1", "file://"+bare, local)

	s := service(root)
	repo, err := s.LocateRelease(context.Background(), local)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.InspectRelease(context.Background(), repo, ReleaseOptions{Push: true}); err == nil ||
		!strings.Contains(err.Error(), "shallow") {
		t.Errorf("err = %v", err)
	}
}

func TestATagStoppedOnceWrittenIsDeletedAgain(t *testing.T) {
	root := isolate(t)
	bare, _ := remote(t, root)
	local := clone(t, bare, filepath.Join(root, "local"))
	// The ref is written, and git then waits on the hook until it is stopped.
	hook := filepath.Join(local, ".git", "hooks", "reference-transaction")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\n[ \"$1\" = committed ] && sleep 30\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	s := service(root)
	st := inspect(t, s, local, ReleaseOptions{Push: true})
	plan, err := st.Plan(core.Choice{})
	if err != nil {
		t.Fatal(err)
	}
	s.Timeout = 3 * time.Second
	if _, err := s.CreateRelease(context.Background(), st, plan, "", true); err == nil {
		t.Fatal("a stopped tag succeeded")
	}
	if err := os.Remove(hook); err != nil {
		t.Fatal(err)
	}
	if tags := run(t, local, nil, "tag", "--list"); tags != "" {
		t.Errorf("tags left: %s", tags)
	}
	if tags := run(t, bare, nil, "tag", "--list"); tags != "" {
		t.Errorf("pushed: %s", tags)
	}
}

func TestAReleasePushesItsTagAlone(t *testing.T) {
	root := isolate(t)
	bare, _ := remote(t, root)
	local := clone(t, bare, filepath.Join(root, "local"))
	run(t, local, nil, "config", "push.followTags", "true")
	run(t, local, nil, "tag", "--annotate", "--message", "not yet", "draft-9.0.0", "HEAD~1")

	s := service(root)
	st := inspect(t, s, local, ReleaseOptions{Push: true})
	plan, err := st.Plan(core.Choice{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateRelease(context.Background(), st, plan, "", true); err != nil {
		t.Fatal(err)
	}
	if tags := run(t, bare, nil, "tag", "--list"); tags != plan.Tag {
		t.Errorf("pushed %q, want %s alone", tags, plan.Tag)
	}
}

func TestOnlyTheTagThisRunMadeIsItsToDelete(t *testing.T) {
	root := isolate(t)
	bare, _ := remote(t, root)
	local := clone(t, bare, filepath.Join(root, "local"))
	commit := run(t, local, nil, "rev-parse", "HEAD")
	run(t, local, nil, "tag", "--annotate", "--message", "Release v1.0.0", "v1.0.0")
	run(t, local, nil, "tag", "v1.0.1")

	signedLooking := "Release v1.0.2\n\n-----BEGIN PGP SIGNATURE-----\nnot one\n-----END PGP SIGNATURE-----"
	run(t, local, nil, "tag", "--annotate", "--cleanup=verbatim", "--message", signedLooking, "v1.0.2")

	tk := service(root).task(context.Background(), repository(local))
	if got := tk.madeTag("refs/tags/v1.0.2", commit, signedLooking); got == "" {
		t.Error("a message that looks signed is not known as this run's")
	}
	if got := tk.madeTag("refs/tags/v1.0.0", commit, "Release v1.0.0"); got != run(t, local, nil, "rev-parse", "refs/tags/v1.0.0") {
		t.Errorf("its own tag = %q", got)
	}
	for _, tc := range []struct{ ref, commit, message string }{
		{"refs/tags/v1.0.0", commit, "Another's release"},
		{"refs/tags/v1.0.0", "0000000000000000000000000000000000000000", "Release v1.0.0"},
		{"refs/tags/v1.0.1", commit, ""}, // lightweight: no tag object of this run's
	} {
		if got := tk.madeTag(tc.ref, tc.commit, tc.message); got != "" {
			t.Errorf("%+v is this run's: %s", tc, got)
		}
	}
}

func TestATagOfATagIsAReleaseOfItsCommit(t *testing.T) {
	root := isolate(t)
	bare, upstream := remote(t, root)
	run(t, upstream, nil, "tag", "--annotate", "--message", "rc", "v1.0.0-rc.1")
	run(t, upstream, nil, "tag", "--annotate", "--message", "release", "v1.0.0", "v1.0.0-rc.1")
	run(t, upstream, nil, "push", "--quiet", "--tags", "origin")
	local := clone(t, bare, filepath.Join(root, "local"))

	if _, err := inspect(t, service(root), local, ReleaseOptions{Push: true}).Plan(core.Choice{}); err == nil ||
		!strings.Contains(err.Error(), "released already") {
		t.Errorf("err = %v", err)
	}
}

func TestATagTheRemoteTookBeforeItsPushFailedIsReleased(t *testing.T) {
	root := isolate(t)
	bare, _ := remote(t, root)
	local := clone(t, bare, filepath.Join(root, "local"))
	// The remote takes the tag, and then keeps the push waiting until it is stopped.
	hook := filepath.Join(bare, "hooks", "post-receive")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	s := service(root)
	st := inspect(t, s, local, ReleaseOptions{Push: true})
	plan, err := st.Plan(core.Choice{})
	if err != nil {
		t.Fatal(err)
	}
	s.Timeout = 3 * time.Second
	release, err := s.CreateRelease(context.Background(), st, plan, "", true)
	if err != nil || !release.Created || !release.Pushed {
		t.Fatalf("release = %+v, err = %v", release, err)
	}
	if tags := run(t, local, nil, "tag", "--list"); tags != plan.Tag {
		t.Errorf("tags here: %q", tags)
	}
}

func TestARemoteThatPushesElsewhereIsRefused(t *testing.T) {
	root := isolate(t)
	bare, _ := remote(t, root)
	local := clone(t, bare, filepath.Join(root, "local"))
	other := filepath.Join(root, "other.git")
	run(t, root, nil, "init", "--quiet", "--bare", other)
	run(t, local, nil, "remote", "set-url", "--push", "origin", other)

	s := service(root)
	repo, err := s.LocateRelease(context.Background(), local)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.InspectRelease(context.Background(), repo, ReleaseOptions{Push: true}); err == nil ||
		!strings.Contains(err.Error(), "pushes somewhere other") {
		t.Errorf("err = %v", err)
	}
	// Tagging here only pushes nowhere.
	if _, err := s.InspectRelease(context.Background(), repo, ReleaseOptions{}); err != nil {
		t.Errorf("without pushing: %v", err)
	}
}

func TestAReleaseIsOfTheRepositoryThePathIsIn(t *testing.T) {
	root := isolate(t)
	bare, _ := remote(t, root)
	local := clone(t, bare, filepath.Join(root, "local"))
	commit(t, local, "tools/run.sh", "echo\n", time.Time{})
	run(t, local, nil, "init", "--quiet", filepath.Join(local, "tools", "helper"))

	repo, err := service(root).LocateRelease(context.Background(), filepath.Join(local, "tools"))
	if err != nil {
		t.Fatal(err)
	}
	if want, _ := filepath.EvalSymlinks(local); repo.Repository.Path != want {
		t.Errorf("located %s, want %s", repo.Repository.Path, want)
	}
	if _, err := service(root).LocateRelease(context.Background(), root); err == nil {
		t.Error("a directory in no repository was located")
	}

	// A name ending in a space is the repository's own, not the one without it.
	spaced := clone(t, bare, filepath.Join(root, "project "))
	clone(t, bare, filepath.Join(root, "project"))
	repo, err = service(root).LocateRelease(context.Background(), spaced)
	if err != nil {
		t.Fatal(err)
	}
	if want, _ := filepath.EvalSymlinks(spaced); repo.Repository.Path != want {
		t.Errorf("located %q, want %q", repo.Repository.Path, want)
	}
}

func TestReadingTheTagsPrunesNone(t *testing.T) {
	root := isolate(t)
	bare, _ := remote(t, root)
	local := clone(t, bare, filepath.Join(root, "local"))
	run(t, local, nil, "config", "fetch.prune", "true")
	run(t, local, nil, "config", "fetch.pruneTags", "true")
	run(t, local, nil, "tag", "v1.0.0")

	inspect(t, service(root), local, ReleaseOptions{Push: true})
	if tags := run(t, local, nil, "tag", "--list"); tags != "v1.0.0" {
		t.Errorf("the tag never pushed was pruned: tags %q", tags)
	}
}

func TestSeveralRemotesAndNoOriginNeedTheRemoteNamed(t *testing.T) {
	root := isolate(t)
	bare, _ := remote(t, root)
	local := clone(t, bare, filepath.Join(root, "local"))
	run(t, local, nil, "remote", "rename", "origin", "upstream")
	run(t, local, nil, "remote", "add", "fork", bare)

	s := service(root)
	repo, err := s.LocateRelease(context.Background(), local)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.InspectRelease(context.Background(), repo, ReleaseOptions{Push: true}); err == nil ||
		!strings.Contains(err.Error(), "--remote") {
		t.Errorf("err = %v", err)
	}
	if _, err := s.InspectRelease(context.Background(), repo, ReleaseOptions{Remote: "upstream", Push: true}); err != nil {
		t.Errorf("with --remote: %v", err)
	}
}

func TestTheBranchCheckedOutIsReadBesideATagOfItsName(t *testing.T) {
	root := isolate(t)
	bare, _ := remote(t, root)
	local := clone(t, bare, filepath.Join(root, "local"))
	run(t, local, nil, "tag", "main", "HEAD~1")

	repo, err := service(root).LocateRelease(context.Background(), local)
	if err != nil || repo.Branch != "main" {
		t.Fatalf("branch = %q, err = %v", repo.Branch, err)
	}
	if st := inspect(t, service(root), local, ReleaseOptions{Push: true}); st.Commit != run(t, local, nil, "rev-parse", "refs/remotes/origin/main") {
		t.Errorf("commit = %s", st.Commit)
	}
}
