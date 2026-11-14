package config

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/andrerfcsantos/orbit/internal/app"
	appconfig "github.com/andrerfcsantos/orbit/internal/app/config"
	"github.com/andrerfcsantos/orbit/internal/cmd/internal/exit"
	platformconfig "github.com/andrerfcsantos/orbit/internal/platform/config"
	"github.com/andrerfcsantos/orbit/internal/transport/tui/styles"
	"github.com/spf13/cobra"
)

// New builds the config command.
func New(provider app.RuntimeProvider) *cobra.Command {
	var allowUnknown bool

	cmd := &cobra.Command{
		Use:   "config [key] [value]",
		Short: "Inspect or set configuration values",
		Long: `Inspect or change configuration.

  orb config              show every setting in effect
  orb config <key>        show one setting, and where it can be set from
  orb config <key> <val>  set a setting and save it for future runs

Values are read from flags, then environment variables (ORBIT_*), then config
files, then defaults. Writes go to the user config file, which is on that lookup
path, so a value set here applies to later runs -- but a flag or environment
variable still overrides it.

Configuration can hold secrets, so a key marked secret is never printed: only
whether it is set.`,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) > 2 {
				return exit.Usage(fmt.Errorf("expected at most 2 arguments (key and value), got %d", len(args)))
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			runtime := provider()
			if runtime == nil {
				return errors.New("runtime is not initialized")
			}
			service := appconfig.Service{Manager: runtime.Config}
			out := cmd.OutOrStdout()
			s := styles.For(out)

			switch len(args) {
			case 0:
				return list(out, s, service)
			case 1:
				return show(out, s, service, args[0])
			default:
				return set(out, s, service, args[0], args[1], allowUnknown)
			}
		},
	}

	cmd.Flags().BoolVar(&allowUnknown, "allow-unknown", false,
		"Allow setting a key orbit does not know about")
	return cmd
}

// secretValueError explains why a secret cannot be set from the command line, and
// names the two paths that carry no such exposure.
func secretValueError(service appconfig.Service, key string) error {
	where := service.UserConfigFile()
	if where == "" {
		where = "the config file"
	}
	return fmt.Errorf(
		"%s holds a secret, so it cannot be set from the command line: the value would be "+
			"recorded in shell history and visible in the process list. Export %s instead, "+
			"or add the key to %s, which orbit writes with 0600 permissions",
		key, service.EnvVarName(key), where)
}

// secretMask stands in for a secret's value. It is fixed-width rather than
// proportional to the value: a mask that revealed the length would still leak
// something about a credential.
const secretMask = "••••••"

// display renders a value for the terminal, masking one the catalog marks secret.
//
// An unknown key is masked when it looks like a credential. Keys are declared by
// the running command, so `orb config` outside `orb git` lists values from the
// config file whose definitions are not loaded -- without this, a key would be
// masked or not depending on which command was invoked.
func display(service appconfig.Service, key string, value any, allowEmptyNote bool) string {
	if !isSecret(service, key) {
		return fmt.Sprintf("%v", value)
	}
	if allowEmptyNote && fmt.Sprintf("%v", value) == "" {
		return "(not set)"
	}
	return secretMask
}

// secretHints match an unknown key that looks like a credential. Matched against
// the lowercased name with separators normalized, so API_TOKEN and
// provider.api_key are caught alongside provider.api-key -- a forward-compatible
// key is exactly the one nobody has classified yet, which is when guessing wrong
// prints a secret. Over-masking a harmless unknown key costs nothing.
//
// ".user" covers login names, which some vendors issue as credentials: a
// provider.user left in a config file after that key was removed would otherwise
// print in clear.
var secretHints = []string{
	"password", "passwd", "pwd", "secret", "token", "credential", "apikey", ".key", ".user",
}

func isSecret(service appconfig.Service, key string) bool {
	if def, known := service.Describe(key); known {
		return def.Secret
	}
	// Separators are normalized to "." so one hint covers api_key, api-key and
	// api.key. ".key" rather than "key" so that a name such as monkey is not swept up.
	normalized := "." + strings.NewReplacer("_", ".", "-", ".").Replace(strings.ToLower(key))
	for _, hint := range secretHints {
		if strings.Contains(normalized, hint) {
			return true
		}
	}
	return false
}

// list prints every resolved setting.
func list(out io.Writer, s styles.Styles, service appconfig.Service) error {
	entries := service.List()
	if len(entries) == 0 {
		fmt.Fprintln(out, s.Subtle.Render("No configuration set."))
		return nil
	}

	width := 0
	for _, entry := range entries {
		if len(entry.Key) > width {
			width = len(entry.Key)
		}
	}
	for _, entry := range entries {
		fmt.Fprintf(out, "%-*s  %s\n", width, s.Key.Render(entry.Key),
			display(service, entry.Key, entry.Value, true))
	}

	// Naming the files that contributed makes an unexpected value traceable
	// without the reader having to know the lookup order.
	if files := service.LoadedFiles(); len(files) > 0 {
		fmt.Fprintln(out)
		fmt.Fprintln(out, s.Subtle.Render("Loaded from (lowest precedence first):"))
		for _, file := range files {
			fmt.Fprintf(out, "%s\n", s.Subtle.Render("  "+file))
		}
	}
	return nil
}

// show prints one setting and the ways it can be set.
func show(out io.Writer, s styles.Styles, service appconfig.Service, key string) error {
	value, isSet := service.Get(key)
	if isSet {
		fmt.Fprintf(out, "%s  %s\n", s.Key.Render(key), display(service, key, value, true))
	} else {
		fmt.Fprintf(out, "%s  %s\n", s.Key.Render(key), s.Subtle.Render("(not set)"))
	}

	def, known := service.Describe(key)
	if !known {
		// Not an error: an unknown key may be one a newer build understands. But say
		// so, since the likeliest explanation is a typo.
		fmt.Fprintln(out)
		fmt.Fprintln(out, s.Subtle.Render("orbit does not know this key; it may be a typo."))
		return nil
	}

	fmt.Fprintln(out)
	if def.Description != "" {
		fmt.Fprintln(out, s.Subtle.Render(def.Description))
	}
	if def.FlagName != "" {
		fmt.Fprintln(out, s.Subtle.Render("  Flag:    --"+def.FlagName))
	}
	fmt.Fprintln(out, s.Subtle.Render("  Env:     "+service.EnvVarName(key)))
	if def.Default != nil {
		fmt.Fprintln(out, s.Subtle.Render(fmt.Sprintf("  Default: %v", def.Default)))
	}
	if def.Owner != "" {
		fmt.Fprintln(out, s.Subtle.Render("  Owner:   "+def.Owner))
	}
	return nil
}

// set persists a value and reports where it was written.
func set(out io.Writer, s styles.Styles, service appconfig.Service, key, value string, allowUnknown bool) error {
	// Credential keys are deliberately flagless because a secret on a command line
	// is recorded in shell history and visible in the process list. Accepting one
	// here as a positional argument would be the same exposure by another route.
	//
	// Gated on the catalog's own declaration, not on isSecret: that hint list is
	// tuned for masking, where over-matching costs nothing, and it would refuse to
	// write a key like future.key that orbit has no reason to think is a
	// credential. Clearing is allowed either way, since an empty value reveals
	// nothing.
	if def, known := service.Describe(key); known && def.Secret && value != "" {
		return exit.Usage(secretValueError(service, key))
	}

	if err := service.Set(key, value, allowUnknown); err != nil {
		// An unknown key is a usage problem with a known remedy, so it exits as one
		// and the error text names the flag that overrides it.
		if errors.Is(err, platformconfig.ErrUnknownKey) {
			return exit.Usage(err)
		}
		return err
	}

	fmt.Fprintf(out, "%s %s %s\n",
		s.Success.Render("Set"), s.Key.Render(key),
		s.Value.Render(display(service, key, value, false)))
	fmt.Fprintf(out, "%s\n", s.Subtle.Render("  written to "+service.UserConfigFile()))

	// On LATER runs a flag or environment variable still outranks the file just
	// written, and a value that appears not to have taken effect is otherwise
	// baffling. Worded for future invocations because that is when it applies: within
	// this process the write sits in viper's override layer, above everything.
	if def, known := service.Describe(key); known {
		hint := "  on future runs, overridden by " + service.EnvVarName(key)
		if def.FlagName != "" {
			hint += " or --" + def.FlagName
		}
		fmt.Fprintf(out, "%s\n", s.Subtle.Render(hint))
	}
	return nil
}
