package version

import (
	"testing"

	"github.com/andrerfcsantos/orbit/internal/platform/build"
)

func TestInfoReturnsBuildMetadata(t *testing.T) {
	info := build.NewInfo("v1.0.0", "abc123", "2026-01-01T00:00:00Z")
	if got := (Service{BuildInfo: info}).Info(); got != *info {
		t.Errorf("Info() = %#v, want %#v", got, *info)
	}
}

func TestInfoWithoutBuildInfoReportsPlaceholders(t *testing.T) {
	// A Service with no build info must describe itself as an unstamped build
	// rather than returning empty strings, which would read as a bug in the caller.
	got := Service{}.Info()
	want := build.Info{
		Version: build.UnknownVersion,
		Commit:  build.UnknownCommit,
		Date:    build.UnknownDate,
	}
	if got != want {
		t.Errorf("Info() = %#v, want %#v", got, want)
	}
}
