package version

import "github.com/andrerfcsantos/orbit/internal/platform/build"

// Service answers questions about the running build.
//
// It is the single implementation behind `orb version`, so any other surface
// added later cannot disagree with it about what is running.
type Service struct {
	BuildInfo *build.Info
}

// Info returns the build metadata. A Service with no BuildInfo reports the
// unknown-build placeholders rather than empty strings, so an unstamped binary is
// self-describing instead of looking like a serialization bug.
func (s Service) Info() build.Info {
	if s.BuildInfo == nil {
		return *build.NewInfo("", "", "")
	}
	return *s.BuildInfo
}
