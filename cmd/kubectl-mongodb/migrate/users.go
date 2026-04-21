package migrate

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

type usersFlags struct {
	configMapName        string
	secretName           string
	namespace            string
	outputFile           string
	usersSecretsFile     string
	resourceNameOverride string
}

var uFlags usersFlags

var UsersCmd = &cobra.Command{
	Use:   "users",
	Short: "Generate MongoDBUser Kubernetes CRs",
	Long: `Generates MongoDBUser Kubernetes CRs from an Ops Manager/Cloud Manager automation
config. The automation config is validated before generation. If any blockers are
found, the command fails without producing output.

Requires a ConfigMap (baseUrl, orgId, projectName) and a Secret (publicKey,
privateKey) in the same format used by the operator.

Example:

kubectl mongodb migrate users --config-map-name my-project --secret-name my-credentials --namespace mongodb`,
	RunE: runGenerateUsers,
}

func init() {
	UsersCmd.Flags().StringVar(&uFlags.configMapName, "config-map-name", "", "Name of the ConfigMap containing the OM connection details (baseUrl, orgId, projectName) (required)")
	UsersCmd.Flags().StringVar(&uFlags.secretName, "secret-name", "", "Name of the Secret containing the OM API credentials (publicKey, privateKey) (required)")
	UsersCmd.Flags().StringVar(&uFlags.namespace, "namespace", defaultNamespace, "Namespace of the ConfigMap and Secret")
	UsersCmd.Flags().StringVarP(&uFlags.outputFile, "output", "o", "", "Write generated CRs to this file instead of stdout")
	UsersCmd.Flags().StringVar(&uFlags.usersSecretsFile, "users-secrets-file", "", "CSV file mapping 'username:database,secret-name' for SCRAM users. When provided, customer-owned Secrets are referenced instead of generated and interactive prompts for user passwords are suppressed")
	UsersCmd.Flags().StringVar(&uFlags.resourceNameOverride, "resource-name-override", "", "Name of the MongoDB CR that users will reference (mongodbResourceRef.name). Defaults to the normalized replica set name from the automation config")
	_ = UsersCmd.MarkFlagRequired("config-map-name")
	_ = UsersCmd.MarkFlagRequired("secret-name")
}

func runGenerateUsers(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()

	conn, _, err := prepareConnection(ctx, uFlags.namespace, uFlags.configMapName, uFlags.secretName)
	if err != nil {
		return err
	}

	_, _, _, err = fetchAndValidate(conn)
	if err != nil {
		return err
	}

	_, _ = fmt.Fprintln(os.Stderr, "Validation passed. CR generation is not yet available in this build.")
	return nil
}
