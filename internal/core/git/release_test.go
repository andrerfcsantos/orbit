package git

import (
	"strings"
	"testing"
	"time"
)

// known makes tags: name@commit, merged unless marked "~", published unless "!".
func known(t *testing.T, specs ...string) []Known {
	t.Helper()
	var tags []Known
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, spec := range specs {
		merged, published := !strings.HasPrefix(spec, "~"), !strings.HasSuffix(spec, "!")
		spec = strings.TrimSuffix(strings.TrimPrefix(spec, "~"), "!")
		name, commit, _ := strings.Cut(spec, "@")
		tag, ok := ParseTag(name)
		if !ok {
			t.Fatalf("%q is no tag", name)
		}
		created = created.Add(time.Hour)
		tags = append(tags, Known{Tag: tag, Commit: commit, Created: created, Merged: merged, Published: published})
	}
	return tags
}

func plan(t *testing.T, r Releases, choice Choice) Planned {
	t.Helper()
	planned, err := r.Next(choice)
	if err != nil {
		t.Fatalf("Next(%+v): %v", choice, err)
	}
	return planned
}

func TestNextBumpsTheLatestReleaseOnTheBranch(t *testing.T) {
	tags := known(t, "v1.2.0@a", "v1.10.0@b", "v1.3.0@c", "~v2.0.0@z", "v1.11.0-rc.1@d")
	r := Releases{Prefix: "v", Tags: tags, Commit: "head"}
	for bump, want := range map[Bump]string{BumpMinor: "v1.11.0", BumpPatch: "v1.10.1", "": "v1.11.0"} {
		got := plan(t, r, Choice{Bump: bump})
		if got.Tag != want || got.Previous != "v1.10.0" {
			t.Errorf("%q: %s after %s, want %s after v1.10.0", bump, got.Tag, got.Previous, want)
		}
	}
	// v2.0.0 is on another branch, and v2.0.0 is the major bump of v1.10.0.
	r.Tags = known(t, "v1.10.0@b", "~v2.0.0@z")
	if _, err := r.Next(Choice{Bump: BumpMajor}); err == nil || !strings.Contains(err.Error(), "not on the branch") {
		t.Errorf("err = %v", err)
	}
}

func TestTheFirstRelease(t *testing.T) {
	r := Releases{Prefix: "v", Commit: "head"}
	for bump, want := range map[Bump]string{BumpMinor: "v0.1.0", BumpPatch: "v0.0.1", BumpMajor: "v1.0.0"} {
		got := plan(t, r, Choice{Bump: bump})
		if got.Tag != want || got.Previous != "" {
			t.Errorf("%s: %+v, want %s", bump, got, want)
		}
	}
}

func TestAnUnpublishedTagDoesNotDecideTheNextVersion(t *testing.T) {
	r := Releases{Prefix: "v", Tags: known(t, "v1.0.0@a", "v1.1.0@b!"), Commit: "head", PublishedOnly: true}
	if _, err := r.Next(Choice{}); err == nil || !strings.Contains(err.Error(), "v1.1.0 exists already") {
		t.Errorf("err = %v", err)
	}
	if got := plan(t, r, Choice{Bump: BumpPatch}); got.Tag != "v1.0.1" {
		t.Errorf("tag = %s", got.Tag)
	}
}

func TestACommitIsReleasedOnce(t *testing.T) {
	r := Releases{Prefix: "v", Tags: known(t, "v1.0.0@head"), Commit: "head"}
	if _, err := r.Next(Choice{}); err == nil || !strings.Contains(err.Error(), "released already") {
		t.Errorf("err = %v", err)
	}
	// A pre-release of the next one is fine.
	if got := plan(t, r, Choice{Stage: "beta"}); got.Tag != "v1.1.0-beta.1" {
		t.Errorf("tag = %s", got.Tag)
	}
	// And a release of a commit a release candidate was made of.
	r.Tags = known(t, "v1.0.0@a", "v1.1.0-rc.1@head")
	if got := plan(t, r, Choice{}); got.Tag != "v1.1.0" {
		t.Errorf("tag = %s", got.Tag)
	}
}

func TestPreReleasesAreNumberedAfterTheirStage(t *testing.T) {
	tags := known(t, "v1.0.0@a", "v1.1.0-beta.1@b", "~v1.1.0-beta.2@c", "v1.1.0-beta9@d", "v1.1.0-rc@e",
		"v1.1.0-fdg.beta.4@f", "v1.2.0-beta.7@g", "v1.1.0-betamax.3@h")
	r := Releases{Prefix: "v", Tags: tags, Commit: "head"}
	for _, tc := range []struct {
		choice Choice
		want   string
	}{
		{Choice{Stage: "beta", Bump: BumpMinor}, "v1.1.0-beta.10"},
		{Choice{Stage: "beta"}, "v1.2.0-beta.8"},
		{Choice{Stage: "rc"}, "v1.1.0-rc.1"},
		{Choice{Stage: "alpha"}, "v1.1.0-alpha.1"},
		{Choice{Stage: "beta", Bump: BumpPatch}, "v1.0.1-beta.1"},
		{Choice{Stage: "feat", Bump: BumpMajor}, "v2.0.0-feat.1"},
	} {
		if got := plan(t, r, tc.choice); got.Tag != tc.want {
			t.Errorf("%+v: %s, want %s", tc.choice, got.Tag, tc.want)
		}
	}
	// Going back to beta after a release candidate is said.
	if got := plan(t, r, Choice{Stage: "alpha"}); len(got.Notes) == 0 {
		t.Errorf("no note that %s comes before the betas", got.Tag)
	}
}

func TestAPreReleaseIsNeverOfAVersionReleased(t *testing.T) {
	// The branch forked before v1.1.0 and v1.2.0 were released.
	r := Releases{Prefix: "v", Tags: known(t, "v1.0.0@a", "~v1.1.0@b", "~v1.2.0@c"), Commit: "head"}
	got := plan(t, r, Choice{Stage: "feat"})
	if got.Tag != "v1.3.0-feat.1" || len(got.Notes) != 2 {
		t.Errorf("%+v", got)
	}
	v, _ := ParseVersion("1.1.0")
	if _, err := r.Next(Choice{Stage: "feat", Version: &v}); err == nil {
		t.Error("pre-released a version released")
	}
}

func TestAnExplicitVersion(t *testing.T) {
	r := Releases{Prefix: "release-", Tags: known(t, "release-1.4.0@a"), Commit: "head"}
	v, _ := ParseVersion("2.0.0")
	if got := plan(t, r, Choice{Version: &v}); got.Tag != "release-2.0.0" {
		t.Errorf("tag = %s", got.Tag)
	}
	if got := plan(t, r, Choice{Version: &v, Stage: "rc"}); got.Tag != "release-2.0.0-rc.1" {
		t.Errorf("tag = %s", got.Tag)
	}
	old, _ := ParseVersion("1.3.9")
	if _, err := r.Next(Choice{Version: &old}); err == nil {
		t.Error("released a version before the latest")
	}
}

func TestChoiceValidate(t *testing.T) {
	pre, _ := ParseVersion("1.0.0-rc.1")
	for _, choice := range []Choice{
		{Stage: "be ta"}, {Stage: "12"}, {Stage: "beta.1"},
		{Stage: "beta", Version: &pre},
	} {
		if err := choice.Validate(); err == nil {
			t.Errorf("%+v is valid", choice)
		}
	}
}

func TestConvention(t *testing.T) {
	tags := known(t, "release-1.0.0@a", "v2.0.0@b", "~api/v9.0.0@c", "v2.1.0@d!")
	if prefix, latest := Convention(tags, true); prefix != "v" || latest.Name != "v2.0.0" {
		t.Errorf("prefix = %q, latest = %v", prefix, latest)
	}
	if prefix, _ := Convention(nil, true); prefix != "v" {
		t.Errorf("prefix = %q", prefix)
	}
	if series := Series(tags, "release-"); len(series) != 1 {
		t.Errorf("series = %v", series)
	}
}

func TestAPreReleaseGoesOnWithItsSeries(t *testing.T) {
	r := Releases{Prefix: "v", Tags: known(t, "v1.4.0@a", "v2.0.0-beta.2@b", "v2.0.0-fdg.beta.1@c", "~v1.5.0-beta.9@d"), Commit: "head"}
	for _, tc := range []struct {
		choice Choice
		want   string
	}{
		{Choice{Stage: "beta"}, "v2.0.0-beta.3"},
		{Choice{Stage: "rc"}, "v1.5.0-rc.1"},
		{Choice{Stage: "beta", Bump: BumpMinor}, "v1.5.0-beta.10"},
		{Choice{}, "v1.5.0"},
	} {
		if got := plan(t, r, tc.choice); got.Tag != tc.want {
			t.Errorf("%+v: %s, want %s", tc.choice, got.Tag, tc.want)
		}
	}
	// Once 2.0.0 is released, its betas are done with.
	r.Tags = append(r.Tags, known(t, "v2.0.0@e")...)
	if got := plan(t, r, Choice{Stage: "beta"}); got.Tag != "v2.1.0-beta.1" {
		t.Errorf("tag = %s", got.Tag)
	}
}

func TestAnExplicitPreReleaseIsNeverOfAVersionReleased(t *testing.T) {
	r := Releases{Prefix: "v", Tags: known(t, "v1.0.0@head", "~v2.0.0@b"), Commit: "head"}
	rc, _ := ParseVersion("2.0.0-rc.1")
	if _, err := r.Next(Choice{Version: &rc}); err == nil || !strings.Contains(err.Error(), "released already") {
		t.Errorf("err = %v", err)
	}
	// A pre-release of a version to come is fine on a commit released already.
	next, _ := ParseVersion("3.0.0-rc.1")
	if got := plan(t, r, Choice{Version: &next}); got.Tag != "v3.0.0-rc.1" {
		t.Errorf("tag = %s", got.Tag)
	}
}

func TestAPrefixMustLeaveTheVersionAsItIs(t *testing.T) {
	v, _ := ParseVersion("2.0.0")
	for _, prefix := range []string{"v1", "v.", "-m", "release 1"} {
		r := Releases{Prefix: prefix, Commit: "head"}
		if got, err := r.Next(Choice{Version: &v}); err == nil {
			t.Errorf("prefix %q made %s", prefix, got.Tag)
		}
	}
	for _, prefix := range []string{"", "v", "release-", "api/v"} {
		if _, err := (Releases{Prefix: prefix, Commit: "head"}).Next(Choice{Version: &v}); err != nil {
			t.Errorf("prefix %q: %v", prefix, err)
		}
	}
}

func TestAPreReleaseNumberTooBigIsNotFollowed(t *testing.T) {
	for _, last := range []string{"v1.1.0-beta.9223372036854775807", "v1.1.0-beta.99999999999999999999999", "v1.1.0-beta.1000000000"} {
		r := Releases{Prefix: "v", Tags: known(t, "v1.0.0@a", last+"@b"), Commit: "head"}
		if got, err := r.Next(Choice{Stage: "beta"}); err == nil {
			t.Errorf("after %s: %s", last, got.Tag)
		}
	}
}

func TestInProgressCountsWhatTheNextOneFollows(t *testing.T) {
	r := Releases{Prefix: "v", Tags: known(t, "v1.0.0@a", "v2.0.0-beta.1@b!", "v1.1.0-rc.1@c", "~v1.2.0-rc.1@d"),
		Commit: "head", PublishedOnly: true}
	under := r.InProgress()
	if len(under) != 1 || under[0].Name != "v1.1.0-rc.1" {
		t.Errorf("in progress = %v", under)
	}
	// What is offered is what is gone on with.
	if got := plan(t, r, Choice{Stage: "beta"}); got.Tag != "v1.1.0-beta.1" {
		t.Errorf("tag = %s", got.Tag)
	}
}
