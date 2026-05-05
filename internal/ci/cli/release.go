package cli

import "github.com/spf13/cobra"

func newReleaseCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "release",
		Short: "Release automation commands",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(newReleasePromoteCmd())
	cmd.AddCommand(newReleaseVerifyCmd())
	cmd.AddCommand(newReleasePRCmd())
	return cmd
}
