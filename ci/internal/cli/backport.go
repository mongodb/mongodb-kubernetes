package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/mongodb/mongodb-kubernetes/ci"
	"github.com/mongodb/mongodb-kubernetes/ci/internal/backport"
)

func newBackportCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backport",
		Short: "Query the backporting-branch chain declared in ci/backporting.yaml",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(newBackportFromCmd())
	return cmd
}

func newBackportFromCmd() *cobra.Command {
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "from <branch>",
		Short: "Print the branch a fix in <branch> should be backported to next",
		Long: "Prints the next branch in the backporting chain after <branch> " +
			"(nothing if <branch> is the last one). Errors if <branch> is not tracked. " +
			"With --json, prints the full target branch entry instead of just its name.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := backport.Parse(ci.BackportingYAML)
			if err != nil {
				return err
			}
			next, err := cfg.NextBranch(args[0])
			if err != nil {
				return err
			}
			if next == nil {
				return nil
			}
			if asJSON {
				encoded, err := json.Marshal(next)
				if err != nil {
					return err
				}
				_, err = fmt.Fprintln(cmd.OutOrStdout(), string(encoded))
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), next.Name)
			return err
		},
	}

	cmd.Flags().BoolVar(&asJSON, "json", false, "print the full target branch entry as JSON instead of just its name")

	return cmd
}
