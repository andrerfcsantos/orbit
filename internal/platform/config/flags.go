package config

import (
	"time"

	"github.com/spf13/cobra"
)

// The helpers below create a cobra flag and register its KeyDef in one call, so
// the flag's default and usage text come from the same declaration the config
// layer resolves against. Declaring them separately is what lets `--help`
// advertise a default the code no longer uses.
//
// Persistent variants attach the flag to the command and all its descendants,
// which is what a cross-cutting setting like --log-level needs.

// RegisterStringFlag adds a string flag for def to cmd and registers the key.
func (m *Manager) RegisterStringFlag(cmd *cobra.Command, def KeyDef) {
	value, _ := def.Default.(string)
	cmd.Flags().StringP(def.FlagName, def.Shorthand, value, def.Description)
	m.RegisterKeys(cmd, def)
}

// RegisterIntFlag adds an int flag for def to cmd and registers the key.
func (m *Manager) RegisterIntFlag(cmd *cobra.Command, def KeyDef) {
	value, _ := def.Default.(int)
	cmd.Flags().IntP(def.FlagName, def.Shorthand, value, def.Description)
	m.RegisterKeys(cmd, def)
}

// RegisterBoolFlag adds a bool flag for def to cmd and registers the key.
func (m *Manager) RegisterBoolFlag(cmd *cobra.Command, def KeyDef) {
	value, _ := def.Default.(bool)
	cmd.Flags().BoolP(def.FlagName, def.Shorthand, value, def.Description)
	m.RegisterKeys(cmd, def)
}

// RegisterDurationFlag adds a duration flag for def to cmd and registers the key.
func (m *Manager) RegisterDurationFlag(cmd *cobra.Command, def KeyDef) {
	value, _ := def.Default.(time.Duration)
	cmd.Flags().DurationP(def.FlagName, def.Shorthand, value, def.Description)
	m.RegisterKeys(cmd, def)
}

// RegisterPersistentStringFlag adds an inheritable string flag and registers the key.
func (m *Manager) RegisterPersistentStringFlag(cmd *cobra.Command, def KeyDef) {
	value, _ := def.Default.(string)
	cmd.PersistentFlags().StringP(def.FlagName, def.Shorthand, value, def.Description)
	m.RegisterKeys(cmd, def)
}

// RegisterPersistentIntFlag adds an inheritable int flag and registers the key.
func (m *Manager) RegisterPersistentIntFlag(cmd *cobra.Command, def KeyDef) {
	value, _ := def.Default.(int)
	cmd.PersistentFlags().IntP(def.FlagName, def.Shorthand, value, def.Description)
	m.RegisterKeys(cmd, def)
}

// RegisterPersistentDurationFlag adds an inheritable duration flag and registers the key.
func (m *Manager) RegisterPersistentDurationFlag(cmd *cobra.Command, def KeyDef) {
	value, _ := def.Default.(time.Duration)
	cmd.PersistentFlags().DurationP(def.FlagName, def.Shorthand, value, def.Description)
	m.RegisterKeys(cmd, def)
}

// RegisterPersistentBoolFlag adds an inheritable bool flag and registers the key.
func (m *Manager) RegisterPersistentBoolFlag(cmd *cobra.Command, def KeyDef) {
	value, _ := def.Default.(bool)
	cmd.PersistentFlags().BoolP(def.FlagName, def.Shorthand, value, def.Description)
	m.RegisterKeys(cmd, def)
}
