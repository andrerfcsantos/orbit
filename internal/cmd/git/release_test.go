package git

import (
	"testing"

	core "github.com/andrerfcsantos/orbit/internal/core/git"
	"github.com/spf13/cobra"
)

func TestReleaseCommandDoesWhatWasAnsweredWithoutAsking(t *testing.T) {
	newCmd := func(args ...string) *cobra.Command {
		cmd := &cobra.Command{}
		for _, name := range []string{"branch", "remote", "prefix", "bump", "pre", "version", "message"} {
			cmd.Flags().String(name, "", "")
		}
		cmd.Flags().Bool("push", true, "")
		if err := cmd.Flags().Parse(args); err != nil {
			t.Fatal(err)
		}
		return cmd
	}
	for _, tc := range []struct {
		flags   []string
		args    []string
		answers releaseAnswers
		dryRun  bool
		want    string
	}{
		{nil, nil, releaseAnswers{choice: &core.Choice{Bump: core.BumpMinor}}, false, "orb git release-tag --bump minor --yes"},
		{nil, nil, releaseAnswers{choice: &core.Choice{Bump: core.BumpMinor}}, true, "orb git release-tag --bump minor --dry-run"},
		{nil, nil, releaseAnswers{choice: &core.Choice{Stage: "beta"}}, false, "orb git release-tag --pre beta --yes"},
		{[]string{"--push=true", "--message", "it's out"}, []string{"-odd dir"}, releaseAnswers{branch: "main", choice: &core.Choice{Bump: core.BumpPatch}}, false,
			"orb git release-tag --branch main --bump patch --push=true --message 'it'\\''s out' --yes -- '-odd dir'"},
		{[]string{"--version", "2.0.0"}, nil, releaseAnswers{}, false, "orb git release-tag --version 2.0.0 --yes"},
	} {
		if got := releaseCommand(newCmd(tc.flags...), tc.args, tc.answers, tc.dryRun); got != tc.want {
			t.Errorf("got  %s\nwant %s", got, tc.want)
		}
	}
}
