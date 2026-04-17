package migrate

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/mongodb/mongodb-kubernetes/controllers/om"
)

const defaultNamespace = "default"

type cliFlags struct {
	configMapName        string
	secretName           string
	namespace            string
	multiClusterNames    string
	outputFile           string
	resourceNameOverride string
}

var flags cliFlags

var MigrateCmd = &cobra.Command{
	Use:   "migrate",
	Short: "Migrate MongoDB deployments to Kubernetes",
	Long: `Generates MongoDB/MongoDBUser Kubernetes CRs from an Ops Manager/Cloud Manager automation
config for migrating existing deployments to the operator. The automation
config is validated automatically before generation. If any blockers are
found, the command fails without producing output.

Requires a ConfigMap (baseUrl, orgId, projectName) and a Secret (publicKey,
privateKey) in the same format used by the operator.

Example:

kubectl mongodb migrate --config-map-name my-project --secret-name my-credentials --namespace mongodb`,
	RunE: runGenerate,
}

func init() {
	MigrateCmd.Flags().StringVar(&flags.configMapName, "config-map-name", "", "Name of the ConfigMap containing the OM connection details (baseUrl, orgId, projectName) (required)")
	MigrateCmd.Flags().StringVar(&flags.secretName, "secret-name", "", "Name of the Secret containing the OM API credentials (publicKey, privateKey) (required)")
	MigrateCmd.Flags().StringVar(&flags.namespace, "namespace", defaultNamespace, "Namespace of the ConfigMap and Secret")
	MigrateCmd.Flags().StringVar(&flags.multiClusterNames, "multi-cluster-names", "", "Comma-separated list of target cluster names (e.g., east1,west1). Generates a MongoDBMultiCluster CR")
	MigrateCmd.Flags().StringVarP(&flags.outputFile, "output", "o", "", "Write generated CRs to this file instead of stdout")
	MigrateCmd.Flags().StringVar(&flags.resourceNameOverride, "resource-name-override", "", "Kubernetes resource name (metadata.name) for the generated CR. When the replica set name is not a valid Kubernetes name it is auto-normalized, but this flag lets you choose a custom name. spec.replicaSetNameOverride is set automatically")
	_ = MigrateCmd.MarkFlagRequired("config-map-name")
	_ = MigrateCmd.MarkFlagRequired("secret-name")
}

func runGenerate(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()

	conn, _, err := prepareConnection(ctx, flags.namespace, flags.configMapName, flags.secretName)
	if err != nil {
		return err
	}

	_, _, _, err = fetchAndValidate(conn)
	if err != nil {
		return err
	}

	// CR generation is implemented in the next PR in the stack.
	_, _ = fmt.Fprintln(os.Stderr, "Validation passed. CR generation is not yet available in this build.")
	return nil
}

// fetchAndValidate reads the automation config and project configs from OM, runs validation,
// and returns the source process to use as the spec template.
func fetchAndValidate(conn om.Connection) (*om.AutomationConfig, *ProjectConfigs, *om.Process, error) {
	ac, err := conn.ReadAutomationConfig()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to read automation config: %w", err)
	}
	projectConfigs, err := readProjectConfigs(conn)
	if err != nil {
		return nil, nil, nil, err
	}
	results, sourceProcess := ValidateMigration(ac, ac.Deployment.ProcessMap(), projectConfigs)
	if errorCount := printValidationResults(os.Stderr, results); errorCount > 0 {
		return nil, nil, nil, fmt.Errorf("validation failed: %d error(s) found. Resolve the issues above before generating Custom Resources", errorCount)
	}
	return ac, projectConfigs, sourceProcess, nil
}

func printValidationResults(w io.Writer, results []ValidationResult) int {
	errorCount := 0
	for _, r := range results {
		if r.Severity == SeverityError {
			errorCount++
		}
		_, _ = fmt.Fprintf(w, "[%s] %s\n\n", r.Severity, r.Message)
	}
	return errorCount
}
