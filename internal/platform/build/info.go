package build

// Info describes the build a running binary came from. The JSON tags are part of
// the contract of `orb version --json`.
//
// It is deliberately a fixed, small set of fields rather than an open map, so what
// it can disclose is bounded by the type.
type Info struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Date    string `json:"date"`
}

// Placeholder values used when a binary is built without ldflags (e.g. a plain
// `go run`). They are exported so tests and callers can recognize an unstamped
// build instead of hardcoding the strings.
const (
	UnknownVersion = "dev"
	UnknownCommit  = "none"
	UnknownDate    = "unknown"
)

// NewInfo builds an Info, substituting the Unknown* placeholders for empty
// values so an unstamped build reports "dev" rather than a blank version.
func NewInfo(version, commit, date string) *Info {
	if version == "" {
		version = UnknownVersion
	}
	if commit == "" {
		commit = UnknownCommit
	}
	if date == "" {
		date = UnknownDate
	}
	return &Info{Version: version, Commit: commit, Date: date}
}
