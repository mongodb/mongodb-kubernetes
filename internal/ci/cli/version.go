package cli

import (
	"fmt"
	"runtime/debug"

	"github.com/spf13/cobra"
)

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print mckctl build information",
		RunE: func(cmd *cobra.Command, _ []string) error {
			info, ok := debug.ReadBuildInfo()
			if !ok {
				_, err := fmt.Fprintln(cmd.OutOrStdout(), "mckctl: build info unavailable")
				return err
			}
			rev, modified := vcsInfo(info)
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "mckctl\n  go:       %s\n  module:   %s\n  revision: %s\n  modified: %t\n",
				info.GoVersion, info.Main.Path, rev, modified)
			return err
		},
	}
}

// vcsInfo extracts the VCS revision and dirty flag from build settings.
// Returns ("", false) if the binary was built without VCS metadata
// (e.g. `go run` from a non-git directory or `-buildvcs=false`).
func vcsInfo(info *debug.BuildInfo) (revision string, modified bool) {
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.modified":
			modified = s.Value == "true"
		}
	}
	return revision, modified
}
