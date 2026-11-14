package git

import (
	"errors"
	"slices"
	"testing"
	"time"
)

func TestParseAge(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		text   string
		cutoff time.Time
		says   string
	}{
		{"6mo", time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC), "6 months"},
		{"1y", time.Date(2025, 10, 21, 12, 0, 0, 0, time.UTC), "1 year"},
		{"90d", time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC), "90 days"},
		{"2w", time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC), "2 weeks"},
		{"36h", time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC), "36 hours"},
		{" 3 Months ", time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC), "3 months"},
	} {
		age, err := ParseAge(tc.text)
		if err != nil {
			t.Errorf("ParseAge(%q): %v", tc.text, err)
			continue
		}
		if got := age.Cutoff(now); !got.Equal(tc.cutoff) {
			t.Errorf("ParseAge(%q).Cutoff = %s, want %s", tc.text, got, tc.cutoff)
		}
		if age.String() != tc.says {
			t.Errorf("ParseAge(%q).String() = %q, want %q", tc.text, age.String(), tc.says)
		}
	}
	// 2562048h is past what a time.Duration holds: it would wrap to a cutoff
	// centuries ahead, and every branch would be older than it.
	for _, text := range []string{"", "6", "mo", "0d", "-1d", "6m", "6 fortnights", "1.5y", "2562048h", "101y", "1201mo",
		"5301w", "36601d", "99999999999999999999d"} {
		if _, err := ParseAge(text); !errors.Is(err, ErrAge) {
			t.Errorf("ParseAge(%q) = %v, want ErrAge", text, err)
		}
	}
	for _, text := range []string{"100y", "1200mo", "5300w", "36600d", "876000h"} {
		age, err := ParseAge(text)
		if err != nil || !age.Cutoff(now).Before(now) {
			t.Errorf("ParseAge(%q): cutoff %s, %v", text, age.Cutoff(now), err)
		}
	}
}

func TestParseStatus(t *testing.T) {
	out := "# branch.oid 1234abcd\x00# branch.head main\x00# branch.upstream origin/main\x00# branch.ab +2 -3\x00" +
		"1 .M N... 100644 100644 100644 aaaa bbbb a file.go\x00" +
		"2 R. N... 100644 100644 100644 aaaa bbbb R100 new name.go\x00old name.go\x00" +
		"u UU N... 100644 100644 100644 100644 aaaa bbbb cccc conflict.go\x00" +
		"? build/\x00"
	status, err := ParseStatus(out)
	if err != nil {
		t.Fatal(err)
	}
	if status.Commit != "1234abcd" || status.Branch != "main" || status.Upstream != "origin/main" {
		t.Errorf("status = %+v", status)
	}
	if !status.Compared || status.Ahead != 2 || status.Behind != 3 {
		t.Errorf("ahead/behind = %v %d %d", status.Compared, status.Ahead, status.Behind)
	}
	want := []string{"a file.go", "new name.go", "old name.go", "conflict.go", "build/"}
	if !slices.Equal(status.Paths, want) {
		t.Errorf("paths = %q, want %q", status.Paths, want)
	}
	if status.Conflicted != 1 {
		t.Errorf("conflicted = %d", status.Conflicted)
	}

	detached, err := ParseStatus("# branch.oid (initial)\x00# branch.head (detached)\x00# branch.upstream origin/gone\x00")
	if err != nil {
		t.Fatal(err)
	}
	if detached.Commit != "" || detached.Branch != "" || detached.Compared || len(detached.Paths) != 0 {
		t.Errorf("detached = %+v", detached)
	}
}

func TestOverlapping(t *testing.T) {
	local := []string{"a.go", "build/", "docs/readme.md", "x"}
	incoming := []string{"build/out.bin", "docs", "b.go", "xy"}
	if got, want := Overlapping(local, incoming), []string{"build", "docs/readme.md"}; !slices.Equal(got, want) {
		t.Errorf("Overlapping = %q, want %q", got, want)
	}
	if got := Overlapping([]string{"a.go"}, []string{"b.go"}); len(got) != 0 {
		t.Errorf("Overlapping = %q, want none", got)
	}
}

func TestParseRefsAndStale(t *testing.T) {
	out := "refs/heads/main\x00aaa\x001700000000\x00refs/remotes/origin/main\x00\x00origin\x00refs/heads/main\n" +
		"refs/heads/old\x00bbb\x001600000000\x00\x00\x00\x00\n" +
		"refs/heads/new\x00ccc\x001800000000\x00\x00\x00\x00\n" +
		"refs/heads/alias\x00aaa\x001600000000\x00\x00refs/heads/main\x00\x00\n"
	refs, err := ParseRefs(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 4 || refs[0].Upstream != "refs/remotes/origin/main" || refs[0].UpstreamRemote != "origin" || refs[0].UpstreamRef != "refs/heads/main" ||
		!refs[1].Date.Equal(time.Unix(1600000000, 0)) || refs[3].Symref != "refs/heads/main" {
		t.Fatalf("refs = %+v", refs)
	}
	stale := Stale(refs, time.Unix(1750000000, 0), func(ref Ref) bool { return ref.Ref == "refs/heads/main" })
	if len(stale) != 1 || stale[0].Ref != "refs/heads/old" {
		t.Errorf("stale = %+v", stale)
	}
	if _, err := ParseRefs("refs/heads/x\x00aaa\n"); err == nil {
		t.Error("a short line was read")
	}
	// An annotated tag fetched among the branches has no commit date.
	tagged, err := ParseRefs("refs/remotes/origin/tags/v1\x00ddd\x00\x00\x00\x00\x00\nrefs/heads/old\x00bbb\x001600000000\x00\x00\x00\x00\n")
	if err != nil || len(tagged) != 1 || tagged[0].Ref != "refs/heads/old" {
		t.Errorf("tagged = %+v, %v", tagged, err)
	}
}

func TestSource(t *testing.T) {
	usual := []string{"+refs/heads/*:refs/remotes/origin/*", "+refs/pull/*/head:refs/remotes/origin/pr/*"}
	archive := []string{"+refs/heads/*:refs/remotes/origin/archive/*"}
	for _, tc := range []struct {
		refspecs []string
		tracking string
		want     string
		ok       bool
	}{
		{usual, "refs/remotes/origin/feature/x", "feature/x", true},
		// A branch called pr/12, or pull request 12: either refspec could have
		// fetched it.
		{usual, "refs/remotes/origin/pr/12", "", false},
		{[]string{"+refs/pull/*/head:refs/remotes/origin/pr/*", "+refs/heads/main:refs/remotes/origin/main"}, "refs/remotes/origin/pr/12", "", false},
		{archive, "refs/remotes/origin/archive/feature", "feature", true},
		{archive, "refs/remotes/origin/feature", "", false},
		{[]string{"refs/heads/main:refs/remotes/origin/main"}, "refs/remotes/origin/main", "main", true},
		{[]string{"^refs/heads/secret", "+refs/heads/*:refs/remotes/origin/*"}, "refs/remotes/origin/a", "a", true},
	} {
		got, ok := Source(tc.refspecs, tc.tracking)
		if got != tc.want || ok != tc.ok {
			t.Errorf("Source(%q, %s) = %q, %v; want %q, %v", tc.refspecs, tc.tracking, got, ok, tc.want, tc.ok)
		}
	}
}

func TestRemoteHead(t *testing.T) {
	if branch, ok := RemoteHead("ref: refs/heads/develop\tHEAD\n7fed839\tHEAD\n"); !ok || branch != "develop" {
		t.Errorf("RemoteHead = %q, %v", branch, ok)
	}
	if _, ok := RemoteHead("7fed839\tHEAD\n"); ok {
		t.Error("a detached remote HEAD names a branch")
	}
	symrefs := RemoteSymrefs("ref: refs/heads/main\tHEAD\n0e5\tHEAD\n0e5\trefs/heads/main\nref: refs/heads/main\trefs/heads/old-alias\n0e5\trefs/heads/old-alias\n")
	if len(symrefs) != 2 || symrefs["refs/heads/old-alias"] != "refs/heads/main" {
		t.Errorf("RemoteSymrefs = %v", symrefs)
	}
	// Protocol v0 says only HEAD is an alias; the tips say the rest.
	tips := RemoteTips("ref: refs/heads/main\tHEAD\n0e5\tHEAD\n0e5\trefs/heads/main\n0e5\trefs/heads/old-alias\n1a2\trefs/heads/old\n1a2\trefs/heads/older\n" +
		"9f8\trefs/heads/tag-alias\n9f8\trefs/tags/saved\n")
	if len(tips) != 7 || tips["refs/heads/old-alias"] != "0e5" {
		t.Errorf("RemoteTips = %v", tips)
	}
	// A branch at a tagged commit loses nothing the tag does not keep, over v2;
	// over v0, it may be the tag's alias.
	shadowed := Shadowed(tips, []string{"old-alias", "old", "older", "tag-alias"})
	if !shadowed["old-alias"] || shadowed["old"] || shadowed["older"] || shadowed["tag-alias"] {
		t.Errorf("Shadowed = %v", shadowed)
	}
	if shadowed := Shadowed(tips, []string{"old"}); !shadowed["old"] {
		t.Errorf("old shares its commit with older, kept: %v", shadowed)
	}
	// A pull request's head sits at its branch's tip, and keeps nothing.
	if shadowed := Shadowed(map[string]string{"refs/heads/feature": "abc", "refs/pull/12/head": "abc"}, []string{"feature"}); shadowed["feature"] {
		t.Errorf("a pull request's ref kept its branch: %v", shadowed)
	}
	if got := Records("/tmp/source.git \n"); !slices.Equal(got, []string{"/tmp/source.git "}) {
		t.Errorf("Records = %q", got)
	}
}

func TestDestinations(t *testing.T) {
	got := Destinations([]string{"+refs/heads/*:refs/remotes/mirror/*", "refs/heads/main:refs/remotes/origin/main",
		"^refs/heads/secret", "+refs/heads/*:refs/heads/*", "+refs/tags/*:refs/tags/*", "+refs/heads/*:refs/remotes/mirror/*",
		"+refs/heads/*:refs/remotes/backup/archive-*"})
	if want := []string{"refs/remotes/mirror/", "refs/remotes/origin/main", "refs/remotes/backup/"}; !slices.Equal(got, want) {
		t.Errorf("Destinations = %q, want %q", got, want)
	}
}

func TestParsePush(t *testing.T) {
	out := "To github.com:fanduel/orbit.git\n" +
		"-\t:refs/heads/old\t[deleted]\n" +
		"!\t:refs/heads/kept\t[remote rejected] (protected branch hook declined)\n" +
		"Done\n"
	pushed := ParsePush(out)
	if !pushed["refs/heads/old"].Done {
		t.Errorf("old = %+v", pushed["refs/heads/old"])
	}
	if got := pushed["refs/heads/kept"]; got.Done || got.Reason != "remote rejected (protected branch hook declined)" {
		t.Errorf("kept = %+v", got)
	}
}

func TestObjectsBytesAndCleaned(t *testing.T) {
	size, err := ObjectsBytes("count: 3\nsize: 12\nin-pack: 40\npacks: 1\nsize-pack: 100\nprune-packable: 0\ngarbage: 0\nsize-garbage: 1\n")
	if err != nil {
		t.Fatal(err)
	}
	if size != 113<<10 {
		t.Errorf("size = %d", size)
	}
	if got, want := Cleaned("Would remove .env\nWould remove node_modules/\n"), []string{".env", "node_modules/"}; !slices.Equal(got, want) {
		t.Errorf("Cleaned = %q", got)
	}
	if got := Cleaned("Removing dist/\n"); !slices.Equal(got, []string{"dist/"}) {
		t.Errorf("Cleaned = %q", got)
	}
	if got := Cleaned("Would remove \"tab\\there\"\nWould remove caf\303\251.txt\n"); !slices.Equal(got, []string{"tab\there", "café.txt"}) {
		t.Errorf("Cleaned = %q", got)
	}
	// A byte that is no UTF-8 stays that byte, raw or escaped.
	if got := Cleaned("Would remove \"legacy-\xff\\t.txt\"\nWould remove \"\\303\\251\\\"q\\\\\"\n"); !slices.Equal(got, []string{"legacy-\xff\t.txt", "é\"q\\"}) {
		t.Errorf("Cleaned = %q", got)
	}
}
