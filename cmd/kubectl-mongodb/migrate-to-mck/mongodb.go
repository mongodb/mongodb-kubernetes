package migratetomck

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

type mongodbFlags struct {
	configMapName        string
	secretName           string
	namespace            string
	outputFile           string
	resourceNameOverride string
	certsSecretPrefix    string
	prometheusSecretName string
}

var mFlags mongodbFlags

var MongodbCmd = &cobra.Command{
	Use:   "mongodb",
	Short: "Generate a MongoDB Kubernetes CR",
	Long: `Generates a MongoDB Kubernetes CR from an Ops Manager/Cloud Manager
automation config. The automation config is validated before generation. If any blockers are
found, the command fails without producing output.

Requires a ConfigMap (baseUrl, orgId, projectName) and a Secret (publicKey,
privateKey) in the same format used by the operator.

Example:

kubectl mongodb migrate mongodb --config-map-name my-project --secret-name my-credentials --namespace mongodb`,
	RunE: runGenerateMongodb,
}

func init() {
	MongodbCmd.Flags().StringVar(&mFlags.configMapName, "config-map-name", "", "Name of the ConfigMap containing the OM connection details (baseUrl, orgId, projectName) (required)")
	MongodbCmd.Flags().StringVar(&mFlags.secretName, "secret-name", "", "Name of the Secret containing the OM API credentials (publicKey, privateKey) (required)")
	MongodbCmd.Flags().StringVar(&mFlags.namespace, "namespace", defaultNamespace, "Namespace of the ConfigMap and Secret")
MongodbCmd.Flags().StringVarP(&mFlags.outputFile, "output", "o", "", "Write generated CRs to this file instead of stdout")
	MongodbCmd.Flags().StringVar(&mFlags.resourceNameOverride, "resource-name-override", "", "Kubernetes resource name (metadata.name) for the generated CR. When the replica set name is not a valid Kubernetes name it is auto-normalized, but this flag lets you choose a custom name. spec.replicaSetNameOverride is set automatically")
	MongodbCmd.Flags().StringVar(&mFlags.certsSecretPrefix, "certs-secret-prefix", "", "Value for spec.security.certsSecretPrefix. Required when TLS is enabled and suppresses the interactive prompt")
	MongodbCmd.Flags().StringVar(&mFlags.prometheusSecretName, "prometheus-secret-name", "", "Name of a pre-created Kubernetes Secret containing the Prometheus password (key: \"password\"). Suppresses the interactive prompt")
	_ = MongodbCmd.MarkFlagRequired("config-map-name")
	_ = MongodbCmd.MarkFlagRequired("secret-name")
}

func runGenerateMongodb(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()

	conn, _, err := prepareConnection(ctx, mFlags.namespace, mFlags.configMapName, mFlags.secretName)
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
