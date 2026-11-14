package git

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// DefaultPrefix is the prefix of a repository's first release tag: v1.0.0.
const DefaultPrefix = "v"

// Stages are the pre-release stages offered. Any other label is a stage too.
var Stages = []string{"alpha", "beta", "rc"}

// Known is a tag of the repository, with where it stands.
type Known struct {
	Tag
	// Commit is the commit the tag points at.
	Commit  string
	Created time.Time
	// Merged is whether the commit being tagged contains the tag's.
	Merged bool
	// Published is whether the remote has the tag.
	Published bool
}

// Releases are the version tags a release is planned from.
type Releases struct {
	// Prefix is the one every tag of the series has.
	Prefix string
	// Tags are every version tag with Prefix, wherever it is.
	Tags []Known
	// Commit is the commit to tag.
	Commit string
	// PublishedOnly counts only the tags the remote has as releases made, so a tag
	// never pushed does not decide the next version.
	PublishedOnly bool
}

// Convention is the prefix the repository's tags follow: that of its latest
// version tag the commit contains, by when it was made, or DefaultPrefix when
// there is none.
func Convention(tags []Known, publishedOnly bool) (string, *Known) {
	var latest *Known
	for i, tag := range tags {
		if !tag.Merged || publishedOnly && !tag.Published {
			continue
		}
		if latest == nil || tag.Created.After(latest.Created) ||
			tag.Created.Equal(latest.Created) && Compare(tag.Version, latest.Version) > 0 {
			latest = &tags[i]
		}
	}
	if latest == nil {
		return DefaultPrefix, nil
	}
	return latest.Prefix, latest
}

// Series are the tags with prefix.
func Series(tags []Known, prefix string) []Known {
	var series []Known
	for _, tag := range tags {
		if tag.Prefix == prefix {
			series = append(series, tag)
		}
	}
	return series
}

func (r Releases) counts(tag Known) bool {
	return tag.Merged && (!r.PublishedOnly || tag.Published)
}

// Latest is the highest release the commit contains: what the next one bumps.
func (r Releases) Latest() (Known, bool) {
	var latest Known
	found := false
	for _, tag := range r.Tags {
		if tag.Version.Stable() && r.counts(tag) && (!found || Compare(tag.Version, latest.Version) > 0) {
			latest, found = tag, true
		}
	}
	return latest, found
}

// Choice is the release asked for.
type Choice struct {
	Bump Bump `json:"bump,omitempty"`
	// Stage makes a pre-release: alpha, beta, rc, or a label of one's own.
	Stage string `json:"stage,omitempty"`
	// Version is the version itself, instead of a bump: with a Stage, the version
	// it is a pre-release of.
	Version *Version `json:"version,omitempty"`
}

var labelPattern = regexp.MustCompile(`^[0-9A-Za-z-]+$`)

// Validate refuses a choice that would make no version, in words free of the
// values given.
func (c Choice) Validate() error {
	if c.Stage != "" {
		if !labelPattern.MatchString(c.Stage) || numeric(c.Stage) {
			return errors.New("the pre-release is a word of letters, digits and hyphens, such as beta or rc")
		}
	}
	if c.Version != nil {
		if c.Version.Build != "" {
			return errors.New("the version cannot have build metadata: it plays no part in which version comes first")
		}
		if c.Version.Pre != "" && c.Stage != "" {
			return errors.New("the version is a pre-release already: leave out the pre-release, or give the version it is one of")
		}
	}
	return nil
}

// Planned is the tag a release makes.
type Planned struct {
	Tag     string  `json:"tag"`
	Version Version `json:"version"`
	// Previous is the release it follows, empty for the first.
	Previous string `json:"previous,omitempty"`
	// Notes are what the person should know about the version chosen.
	Notes []string `json:"notes,omitempty"`
}

// Next plans the tag a choice makes: the latest release the commit contains,
// bumped, or 0.0.0 bumped for the first. A pre-release is numbered after those of
// its version and stage there are anywhere, and is never one of a version already
// released. A release is never made of a commit released already.
func (r Releases) Next(choice Choice) (Planned, error) {
	if err := choice.Validate(); err != nil {
		return Planned{}, err
	}
	var plan Planned
	latest, found := r.Latest()
	if found {
		plan.Previous = latest.Name
	}

	var version Version
	switch {
	case choice.Version != nil:
		version = *choice.Version
		if found && Compare(version, latest.Version) <= 0 {
			return Planned{}, fmt.Errorf("%s does not come after %s, the latest release on the branch", r.Prefix+version.String(), latest.Name)
		}
	case choice.Bump == "" && choice.Stage != "" && r.continuing(choice) != nil:
		// No bump asked for, a series of the stage under way goes on.
		version = r.continuing(choice).Version.Core()
	default:
		bump := choice.Bump
		if bump == "" {
			bump = BumpMinor
		}
		version = latest.Version.Next(bump)
		if choice.Stage == "" {
			break
		}
		// A branch behind the one releasing pre-releases the next version not
		// released yet: a pre-release of one released would come before it.
		for {
			released, ok := r.released(version)
			if !ok {
				break
			}
			next := version.Next(bump)
			plan.Notes = append(plan.Notes, fmt.Sprintf("%s is released already, as %s, which the branch does not have: pre-releasing %s instead",
				version, released.Name, next))
			version = next
		}
	}

	if choice.Stage != "" {
		pre, err := r.preRelease(version, choice.Stage)
		if err != nil {
			return Planned{}, err
		}
		version.Pre = pre
	}
	if version.Stable() {
		if commit := r.releasedAt(r.Commit); commit != nil {
			return Planned{}, fmt.Errorf("the commit is released already, as %s: there is nothing new to release", commit.Name)
		}
	} else if released, ok := r.released(version); ok {
		return Planned{}, fmt.Errorf("%s is released already, as %s: a pre-release of it would come before it", version.Core(), released.Name)
	}

	plan.Version = version
	plan.Tag = r.Prefix + version.String()
	// The tag must read back as the version it was made for: v1 before 2.0.0 is
	// v12.0.0, and a leading - is an option to git.
	if read, ok := ParseTag(plan.Tag); !ok || read.Prefix != r.Prefix || read.Version.String() != version.String() ||
		strings.HasPrefix(plan.Tag, "-") {
		return Planned{}, errors.New("the prefix would make the tag read as another version: " +
			"a prefix cannot start with -, nor end in a digit or a dot")
	}
	for _, tag := range r.Tags {
		if Compare(tag.Version, version) == 0 {
			where := "not on the branch"
			if tag.Merged {
				where = "on the branch"
			}
			return Planned{}, fmt.Errorf("%s exists already, as %s at %s (%s)", plan.Tag, tag.Name, short(tag.Commit), where)
		}
	}
	var after *Known
	for i, tag := range r.Tags {
		if r.counts(tag) && !tag.Version.Stable() && tag.Version.Core() == version.Core() && Compare(tag.Version, version) > 0 &&
			(after == nil || Compare(tag.Version, after.Version) > 0) {
			after = &r.Tags[i]
		}
	}
	if after != nil {
		plan.Notes = append(plan.Notes, fmt.Sprintf("%s comes before %s, which the branch has already", plan.Tag, after.Name))
	}
	return plan, nil
}

// continuing is the highest pre-release of the choice's stage the
// commit contains of a version not released yet, after the latest release.
func (r Releases) continuing(choice Choice) *Known {
	for _, tag := range r.InProgress() {
		if _, ok := stageNumber(tag.Version, choice.Stage); ok {
			return &tag
		}
	}
	return nil
}

// InProgress are the pre-releases under way, highest first: those the commit
// contains (and the remote has, when only those count) after the latest
// release, of versions not released yet. A choice of their stage with no bump
// goes on with them.
func (r Releases) InProgress() []Known {
	latest, found := r.Latest()
	var under []Known
	for _, tag := range r.Tags {
		if tag.Version.Stable() || !r.counts(tag) || found && Compare(tag.Version, latest.Version) <= 0 {
			continue
		}
		if _, ok := r.released(tag.Version); !ok {
			under = append(under, tag)
		}
	}
	slices.SortFunc(under, func(a, b Known) int { return Compare(b.Version, a.Version) })
	return under
}

// released is the release of a version, wherever it is.
func (r Releases) released(v Version) (Known, bool) {
	for _, tag := range r.Tags {
		if tag.Version.Stable() && tag.Version.Core() == v.Core() {
			return tag, true
		}
	}
	return Known{}, false
}

// releasedAt is the release at commit.
func (r Releases) releasedAt(commit string) *Known {
	for i, tag := range r.Tags {
		if tag.Version.Stable() && tag.Commit == commit {
			return &r.Tags[i]
		}
	}
	return nil
}

// preRelease is the pre-release identifier after the highest of version's with
// stage: stage.N, from 1. Those numbered as stageN or not at all are counted
// too, so a series begun by hand moves on.
func (r Releases) preRelease(version Version, stage string) (string, error) {
	highest := 0
	for _, tag := range r.Tags {
		if tag.Version.Core() != version.Core() {
			continue
		}
		if n, ok := stageNumber(tag.Version, stage); ok {
			highest = max(highest, n)
		}
	}
	if highest >= maxCounter {
		return "", errors.New("the last pre-release of the stage has a number too big to follow: give the version with --version")
	}
	return stage + "." + strconv.Itoa(highest+1), nil
}

// maxCounter bounds the pre-release numbers followed: one past it, or one too
// long to read at all, is no series to go on with.
const maxCounter = 1_000_000_000

// stageNumber is the number of a pre-release of stage, if v is one.
func stageNumber(v Version, stage string) (int, bool) {
	if v.Stable() {
		return 0, false
	}
	return counter(strings.Split(v.Pre, "."), stage)
}

// counter reads a stage's number: stage.N, stageN, or stage alone as 0.
func counter(ids []string, stage string) (int, bool) {
	head, ok := cutPrefixFold(ids[0], stage)
	if !ok {
		return 0, false
	}
	switch {
	case head == "" && len(ids) == 1:
		return 0, true
	case head == "" && len(ids) == 2 && numeric(ids[1]):
		return number(ids[1]), true
	case head != "" && len(ids) == 1 && numeric(head):
		return number(head), true
	}
	return 0, false
}

// number reads digits, as maxCounter when they are more than it.
func number(digits string) int {
	n, err := strconv.Atoi(digits)
	if err != nil || n > maxCounter {
		return maxCounter
	}
	return n
}

func cutPrefixFold(s, prefix string) (string, bool) {
	if len(s) < len(prefix) || !strings.EqualFold(s[:len(prefix)], prefix) {
		return s, false
	}
	return s[len(prefix):], true
}

func short(commit string) string {
	if len(commit) > 7 {
		return commit[:7]
	}
	return commit
}
