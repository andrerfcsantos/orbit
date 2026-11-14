package config

import platformconfig "github.com/andrerfcsantos/orbit/internal/platform/config"

// Service reads and writes configuration.
type Service struct {
	Manager *platformconfig.Manager
}

// List returns every resolved setting, sorted by key.
func (s Service) List() []platformconfig.Entry {
	if s.Manager == nil {
		return nil
	}
	return s.Manager.List()
}

// Get returns one value and whether it is set.
func (s Service) Get(key string) (any, bool) {
	if s.Manager == nil {
		return nil, false
	}
	return s.Manager.Get(key)
}

// Describe returns a key's definition, so the CLI can show where a value can be
// set from and who owns it.
func (s Service) Describe(key string) (platformconfig.KeyDef, bool) {
	if s.Manager == nil {
		return platformconfig.KeyDef{}, false
	}
	return s.Manager.FindKeyDef(key)
}

// Set persists a value to the user config file. Unknown keys are refused unless
// allowUnknown is set.
func (s Service) Set(key, value string, allowUnknown bool) error {
	if s.Manager == nil {
		return errNoManager
	}
	return s.Manager.Set(key, value, allowUnknown)
}

// EnvVarName returns the environment variable that sets a key.
func (s Service) EnvVarName(key string) string {
	if s.Manager == nil {
		return ""
	}
	return s.Manager.EnvVarName(key)
}

// UserConfigFile returns the file Set writes to.
func (s Service) UserConfigFile() string {
	if s.Manager == nil {
		return ""
	}
	return s.Manager.UserConfigFile()
}

// LoadedFiles returns the config files that contributed to the current values,
// lowest precedence first.
func (s Service) LoadedFiles() []string {
	if s.Manager == nil {
		return nil
	}
	return s.Manager.LoadedFiles()
}
