// Package cli builds the cobra command tree for mckctl.
//
// NewRoot is the single entry point: cmd/mckctl/main.go calls it, and tests
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
		Use:          "mckctl",
		Short:        "MCK developer tooling",
		Long:         "mckctl centralizes MCK developer tooling so it can be invoked identically from local shells, GitHub Actions, and Evergreen.",
		SilenceUsage: true,
		// No subcommand selected: print help and succeed instead of returning
		// flag.ErrHelp (which would otherwise propagate as exit 1 from main).
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}

	root.AddCommand(newVersionCmd())

	return root
}
