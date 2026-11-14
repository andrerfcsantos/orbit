package config

import "time"

// Keys for `orb git`.
const (
	KeyGitConcurrency     = "git.concurrency"
	KeyGitTimeout         = "git.timeout"
	KeyGitJSON            = "git.json"
	KeyGitBranchOlderThan = "git.branch-delete.older-than"

	KeyGitReleaseTagBump    = "git.release-tag.bump"
	KeyGitReleaseTagPre     = "git.release-tag.pre"
	KeyGitReleaseTagPrefix  = "git.release-tag.prefix"
	KeyGitReleaseTagBranch  = "git.release-tag.branch"
	KeyGitReleaseTagRemote  = "git.release-tag.remote"
	KeyGitReleaseTagPush    = "git.release-tag.push"
	KeyGitReleaseTagMessage = "git.release-tag.message"
)

const gitOwner = "app.git"

var (
	GitConcurrencyKey = KeyDef{
		Key:       KeyGitConcurrency,
		FlagName:  "concurrency",
		Shorthand: "j",
		Description: "How many repositories to work on at once; 0 picks what suits the command " +
			"(16 for update, half the cores for gc and clean)",
		Default: 0,
		Owner:   gitOwner,
	}
	GitTimeoutKey = KeyDef{
		Key:         KeyGitTimeout,
		FlagName:    "timeout",
		Description: "Longest each repository's work may take, 0 for no limit",
		Default:     10 * time.Minute,
		Owner:       gitOwner,
	}
	GitJSONKey = KeyDef{
		Key:         KeyGitJSON,
		FlagName:    "json",
		Description: "Print each repository's result as JSON instead of the progress and the summary",
		Default:     false,
		Owner:       gitOwner,
	}
	GitBranchOlderThanKey = KeyDef{
		Key:         KeyGitBranchOlderThan,
		FlagName:    "older-than",
		Description: "Delete branches whose last commit is older than this: a number and h, d, w, mo or y, such as 90d or 1y",
		Default:     "6mo",
		Owner:       gitOwner,
	}
)

// The keys of `orb git release-tag`. A setting given explicitly is never asked
// for in a terminal; a default is, with the default preselected.
var (
	GitReleaseTagBumpKey = KeyDef{
		Key:         KeyGitReleaseTagBump,
		FlagName:    "bump",
		Description: "Which part of the latest release to bump: major, minor or patch",
		Default:     "minor",
		Owner:       gitOwner,
	}
	GitReleaseTagPreKey = KeyDef{
		Key:      KeyGitReleaseTagPre,
		FlagName: "pre",
		Description: "Make a pre-release of the bumped version at this stage, numbered after the last: " +
			"alpha, beta, rc or a word of your own (v1.5.0-beta.3)",
		Default: "",
		Owner:   gitOwner,
	}
	GitReleaseTagPrefixKey = KeyDef{
		Key:      KeyGitReleaseTagPrefix,
		FlagName: "prefix",
		Description: "What comes before the version in the tag, such as v or release-; " +
			"when not given, the latest tag's, or v for the first",
		Default: "",
		Owner:   gitOwner,
	}
	GitReleaseTagBranchKey = KeyDef{
		Key:         KeyGitReleaseTagBranch,
		FlagName:    "branch",
		Description: "The branch whose tip on the remote is tagged; the branch checked out when not given",
		Default:     "",
		Owner:       gitOwner,
	}
	GitReleaseTagRemoteKey = KeyDef{
		Key:         KeyGitReleaseTagRemote,
		FlagName:    "remote",
		Description: "The remote the branch is read from and the tag pushed to; origin, or the only remote, when not given",
		Default:     "",
		Owner:       gitOwner,
	}
	GitReleaseTagPushKey = KeyDef{
		Key:         KeyGitReleaseTagPush,
		FlagName:    "push",
		Description: "Push the tag to the remote once it is made; --push=false only tags here",
		Default:     true,
		Owner:       gitOwner,
	}
	GitReleaseTagMessageKey = KeyDef{
		Key:         KeyGitReleaseTagMessage,
		FlagName:    "message",
		Shorthand:   "m",
		Description: "The annotated tag's message; \"Release <tag>\" when not given",
		Default:     "",
		Owner:       gitOwner,
	}
)

func gitKeys() []KeyDef {
	return []KeyDef{GitConcurrencyKey, GitTimeoutKey, GitJSONKey, GitBranchOlderThanKey,
		GitReleaseTagBumpKey, GitReleaseTagPreKey, GitReleaseTagPrefixKey,
		GitReleaseTagBranchKey, GitReleaseTagRemoteKey, GitReleaseTagPushKey, GitReleaseTagMessageKey}
}
