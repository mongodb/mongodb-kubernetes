// Package cli builds the cobra command tree for mckci.
//
// NewRoot is the single entry point: ci/cmd/mckci/main.go calls it, and tests
// instantiate fresh root commands per case. Subcommands register themselves
// here in NewRoot; their implementations live in sibling files (one file per
// subcommand) so they stay independently testable.
package cli

import (
	"github.com/spf13/cobra"
)

// NewRoot constructs a fresh root command with all subcommands registered.
// It returns a new instance on every call so tests can run in parallel
// without shared global cobra state.
func NewRoot() *cobra.Command {
	root := &cobra.Command{
		Use:          "mckci",
		Short:        "MCK CI tooling",
		Long:         "mckci centralizes MCK CI tooling so it can be invoked identically from local shells and Evergreen.",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}

	root.AddCommand(newVersionCmd())
	root.AddCommand(newReleaseCmd())

	return root
}
