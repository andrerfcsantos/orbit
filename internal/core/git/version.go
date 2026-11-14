package git

import (
	"cmp"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Version is a semantic version (semver.org, 2.0.0).
type Version struct {
	Major, Minor, Patch int
	// Pre is the pre-release, without its "-": "rc.1", empty for a release.
	Pre string
	// Build is the build metadata, without its "+". It has no bearing on
	// precedence.
	Build string
}

// Stable reports whether the version is a release rather than a pre-release.
func (v Version) Stable() bool { return v.Pre == "" }

// Core is the version without its pre-release and build metadata.
func (v Version) Core() Version { return Version{Major: v.Major, Minor: v.Minor, Patch: v.Patch} }

func (v Version) String() string {
	s := fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	if v.Pre != "" {
		s += "-" + v.Pre
	}
	if v.Build != "" {
		s += "+" + v.Build
	}
	return s
}

// MarshalText writes the version as semver does.
func (v Version) MarshalText() ([]byte, error) { return []byte(v.String()), nil }

// identifier is one dot-separated part of a pre-release or build metadata.
const identifier = `[0-9A-Za-z-]+`

// tagPattern reads a tag as a prefix and a semantic version. The prefix is the
// shortest that leaves a version: empty, or ending in neither a digit nor a dot,
// so 1.2.3.4 is no version and v10.0.0 is v and 10.0.0.
var tagPattern = regexp.MustCompile(`^(|.*?[^0-9.])` +
	`(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)` +
	`(?:-(` + identifier + `(?:\.` + identifier + `)*))?` +
	`(?:\+(` + identifier + `(?:\.` + identifier + `)*))?$`)

// Tag is a tag read as a prefix and a version: release-1.2.3 is "release-" and
// 1.2.3.
type Tag struct {
	Name    string
	Prefix  string
	Version Version
}

// ParseTag reads a tag name as a prefix and a semantic version, and reports
// whether it is one.
func ParseTag(name string) (Tag, bool) {
	match := tagPattern.FindStringSubmatch(name)
	if match == nil || strings.ContainsAny(match[1], " \t\n") {
		return Tag{}, false
	}
	v := Version{Pre: match[5], Build: match[6]}
	var err error
	for i, part := range []*int{&v.Major, &v.Minor, &v.Patch} {
		if *part, err = strconv.Atoi(match[2+i]); err != nil {
			return Tag{}, false
		}
	}
	// semver forbids leading zeros in a numeric pre-release identifier.
	for _, id := range strings.Split(v.Pre, ".") {
		if len(id) > 1 && id[0] == '0' && numeric(id) {
			return Tag{}, false
		}
	}
	return Tag{Name: name, Prefix: match[1], Version: v}, true
}

// ParseVersion reads a version with no prefix, or with a v.
func ParseVersion(text string) (Version, error) {
	tag, ok := ParseTag(strings.TrimSpace(text))
	if !ok || tag.Prefix != "" && tag.Prefix != "v" {
		return Version{}, errors.New("it is not a semantic version, such as 1.4.0 or 2.0.0-rc.1")
	}
	return tag.Version, nil
}

// Compare orders versions by semver precedence: -1 when a comes first, 0 when
// they are equal, 1 when b does. Build metadata is ignored.
func Compare(a, b Version) int {
	if c := cmp.Compare(a.Major, b.Major); c != 0 {
		return c
	}
	if c := cmp.Compare(a.Minor, b.Minor); c != 0 {
		return c
	}
	if c := cmp.Compare(a.Patch, b.Patch); c != 0 {
		return c
	}
	switch {
	case a.Pre == b.Pre:
		return 0
	case a.Pre == "":
		return 1
	case b.Pre == "":
		return -1
	}
	as, bs := strings.Split(a.Pre, "."), strings.Split(b.Pre, ".")
	for i := range min(len(as), len(bs)) {
		if c := compareIdentifiers(as[i], bs[i]); c != 0 {
			return c
		}
	}
	return cmp.Compare(len(as), len(bs))
}

// compareIdentifiers orders pre-release identifiers: numeric ones numerically
// and before alphanumeric ones, which are ordered as ASCII.
func compareIdentifiers(a, b string) int {
	an, bn := numeric(a), numeric(b)
	switch {
	case an && bn:
		if c := cmp.Compare(len(a), len(b)); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	case an:
		return -1
	case bn:
		return 1
	}
	return strings.Compare(a, b)
}

func numeric(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// Bump is which part of a version a release moves on.
type Bump string

const (
	BumpMajor Bump = "major"
	BumpMinor Bump = "minor"
	BumpPatch Bump = "patch"
)

// Bumps are the bumps, in the order they are offered.
var Bumps = []Bump{BumpMinor, BumpPatch, BumpMajor}

// ParseBump reads a bump.
func ParseBump(text string) (Bump, error) {
	bump := Bump(strings.ToLower(strings.TrimSpace(text)))
	switch bump {
	case BumpMajor, BumpMinor, BumpPatch:
		return bump, nil
	}
	return "", errors.New("give major, minor or patch")
}

// Next is the release after v that bump makes: 1.4.2 is 1.5.0 for minor.
func (v Version) Next(bump Bump) Version {
	next := v.Core()
	switch bump {
	case BumpMajor:
		next = Version{Major: v.Major + 1}
	case BumpMinor:
		next = Version{Major: v.Major, Minor: v.Minor + 1}
	case BumpPatch:
		next.Patch++
	}
	return next
}
