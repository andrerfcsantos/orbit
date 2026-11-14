package version

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/andrerfcsantos/orbit/internal/app"
	appversion "github.com/andrerfcsantos/orbit/internal/app/version"
	"github.com/andrerfcsantos/orbit/internal/transport/tui/styles"
	"github.com/spf13/cobra"
)

// New builds the version command.
func New(provider app.RuntimeProvider) *cobra.Command {
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print the version, commit and build date of this binary",
		Long: `Print build metadata for this binary.

A binary built with "make build", or released with GoReleaser, carries the
version tag, commit and build date it was made from; "go run" and a plain
"go build" report dev.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			runtime := provider()
			if runtime == nil {
				return errors.New("runtime is not initialized")
			}
			info := appversion.Service{BuildInfo: runtime.BuildInfo}.Info()
			out := cmd.OutOrStdout()

			// --json exists so scripts can read this without parsing styled output.
			if asJSON {
				encoder := json.NewEncoder(out)
				encoder.SetIndent("", "  ")
				return encoder.Encode(info)
			}

			// Styles are derived from the writer, so piping this yields plain text.
			s := styles.For(out)
			fmt.Fprintf(out, "%s %s\n", s.Key.Render("Version:"), s.Value.Render(info.Version))
			fmt.Fprintf(out, "%s  %s\n", s.Key.Render("Commit:"), s.Value.Render(info.Commit))
			fmt.Fprintf(out, "%s   %s\n", s.Key.Render("Built:"), s.Value.Render(info.Date))
			return nil
		},
	}

	cmd.Flags().BoolVar(&asJSON, "json", false, "Print build metadata as JSON")
	return cmd
}
