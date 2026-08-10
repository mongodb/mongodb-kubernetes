package cli

import (
	"encoding/base64"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mongodb/mongodb-kubernetes/ci/internal/github"
)

func newGHAppTokenCmd() *cobra.Command {
	var (
		appID          string
		installationID string
		pemBase64      string
		pemFile        string
	)

	cmd := &cobra.Command{
		Use:   "app-token",
		Short: "Mint a short-lived GitHub App installation access token and print it to stdout",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if appID == "" || installationID == "" || (pemBase64 == "" && pemFile == "") {
				return fmt.Errorf("--app-id, --installation-id, and --pem-base64 (or --pem-file) are required")
			}

			pem, err := resolvePEM(pemFile, pemBase64)
			if err != nil {
				return err
			}

			token, err := github.MintInstallationToken(cmd.Context(), github.AppTokenInputs{
				AppID:          appID,
				InstallationID: installationID,
				PEM:            pem,
			})
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), token)
			return err
		},
	}

	cmd.Flags().StringVar(&appID, "app-id", "", "GitHub App ID")
	cmd.Flags().StringVar(&installationID, "installation-id", "", "installation ID the token is scoped to")
	cmd.Flags().StringVar(&pemBase64, "pem-base64", "", "base64-encoded PEM private key")
	cmd.Flags().StringVar(&pemFile, "pem-file", "", "path to a PEM private key file")

	return cmd
}

func resolvePEM(pemFile, pemBase64 string) ([]byte, error) {
	if pemFile != "" {
		return os.ReadFile(pemFile)
	}
	if pemBase64 == "" {
		return nil, fmt.Errorf("no private key provided: pass --pem-file or --pem-base64")
	}
	pem, err := base64.StdEncoding.DecodeString(strings.TrimSpace(pemBase64))
	if err != nil {
		return nil, fmt.Errorf("decode --pem-base64: %w", err)
	}
	return pem, nil
}
