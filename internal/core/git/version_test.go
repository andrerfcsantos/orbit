package git

import (
	"slices"
	"testing"
)

func TestParseTag(t *testing.T) {
	for _, tc := range []struct {
		name, prefix, version string
	}{
		{"1.2.3", "", "1.2.3"},
		{"v1.2.3", "v", "1.2.3"},
		{"v10.0.0", "v", "10.0.0"},
		{"release-1.2.3", "release-", "1.2.3"},
		{"release/v2.0.0", "release/v", "2.0.0"},
		{"api/v0.3.1", "api/v", "0.3.1"},
		{"v1.2.3-rc.1", "v", "1.2.3-rc.1"},
		{"v1.2.3-beta1", "v", "1.2.3-beta1"},
		{"v1.2.3-rc.1+build.7", "v", "1.2.3-rc.1+build.7"},
		{"v1.2.3-x2.3.4", "v", "1.2.3-x2.3.4"},
	} {
		tag, ok := ParseTag(tc.name)
		if !ok {
			t.Errorf("ParseTag(%q) is no tag", tc.name)
			continue
		}
		if tag.Prefix != tc.prefix || tag.Version.String() != tc.version {
			t.Errorf("ParseTag(%q) = %q %q, want %q %q", tc.name, tag.Prefix, tag.Version, tc.prefix, tc.version)
		}
	}
	for _, name := range []string{"", "latest", "v1.2", "1.2.3.4", "v01.2.3", "v1.2.3-", "v1.2.3-rc.01", "v1.2.3-rc..1", "my tag 1.2.3"} {
		if tag, ok := ParseTag(name); ok {
			t.Errorf("ParseTag(%q) = %+v, want no tag", name, tag)
		}
	}
}

func TestCompareFollowsSemverPrecedence(t *testing.T) {
	// semver.org's own example, in order.
	ordered := []string{"1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta", "1.0.0-beta.2",
		"1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "1.0.1", "1.1.0", "2.0.0"}
	var versions []Version
	for _, text := range ordered {
		v, err := ParseVersion(text)
		if err != nil {
			t.Fatal(err)
		}
		versions = append(versions, v)
	}
	shuffled := slices.Clone(versions)
	slices.Reverse(shuffled)
	slices.SortFunc(shuffled, Compare)
	for i := range versions {
		if shuffled[i].String() != versions[i].String() {
			t.Fatalf("sorted = %v, want %v", shuffled, versions)
		}
	}
	a, _ := ParseVersion("1.0.0+one")
	b, _ := ParseVersion("1.0.0+two")
	if Compare(a, b) != 0 {
		t.Error("build metadata changed precedence")
	}
}

func TestNext(t *testing.T) {
	v, _ := ParseVersion("1.4.2-rc.1+abc")
	for bump, want := range map[Bump]string{BumpMajor: "2.0.0", BumpMinor: "1.5.0", BumpPatch: "1.4.3"} {
		if got := v.Next(bump).String(); got != want {
			t.Errorf("Next(%s) = %s, want %s", bump, got, want)
		}
	}
}
