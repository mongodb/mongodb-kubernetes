package migrate

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mongodb/mongodb-kubernetes/controllers/om"
)

const defaultNamespace = "default"

var (
	configMapName          string
	secretName             string
	namespace              string
	multiClusterNames      string
	outputFile             string
	replicaSetNameOverride string
)

var MigrateCmd = &cobra.Command{
	Use:   "migrate",
	Short: "Migrate MongoDB deployments to Kubernetes",
	Long: `Generates MongoDB/MongoDBUser Kubernetes CRs from an Ops Manager/Cloud Manager automation
config for migrating existing deployments to the operator. The automation
config is validated automatically before generation; if any blockers are
found the command fails without producing output.

Requires a ConfigMap (baseUrl, orgId, projectName) and a Secret (publicKey,
privateKey) in the same format used by the operator.

Example:

kubectl mongodb migrate --config-map-name my-project --secret-name my-credentials --namespace mongodb`,
	RunE: runGenerate,
}

func init() {
	MigrateCmd.Flags().StringVar(&configMapName, "config-map-name", "", "Name of the ConfigMap containing the OM connection details (baseUrl, orgId, projectName) (required)")
	MigrateCmd.Flags().StringVar(&secretName, "secret-name", "", "Name of the Secret containing the OM API credentials (publicKey, privateKey) (required)")
	MigrateCmd.Flags().StringVar(&namespace, "namespace", defaultNamespace, "Namespace of the ConfigMap and Secret")
	MigrateCmd.Flags().StringVar(&multiClusterNames, "multi-cluster-names", "", "Comma-separated list of target cluster names (e.g., east1,west1); generates a MongoDBMultiCluster CR")
	MigrateCmd.Flags().StringVarP(&outputFile, "output", "o", "", "Write generated CRs to this file instead of stdout")
	MigrateCmd.Flags().StringVar(&replicaSetNameOverride, "replicaset-name-override", "", "Kubernetes resource name for the generated CR; required when the replica set name is not a valid Kubernetes name (sets spec.replicaSetNameOverride automatically)")
	_ = MigrateCmd.MarkFlagRequired("config-map-name")
	_ = MigrateCmd.MarkFlagRequired("secret-name")
}

func runGenerate(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	conn, _, err := prepareConnection(ctx, namespace, configMapName, secretName)
	if err != nil {
		return err
	}
	ac, err := conn.ReadAutomationConfig()
	if err != nil {
		return fmt.Errorf("failed to read automation config: %w", err)
	}

	projectConfigs, err := readProjectConfigs(conn)
	if err != nil {
		return err
	}

	processMap := ac.Deployment.ProcessMap()
	results, _ := ValidateMigration(ac, processMap, projectConfigs)
	if errorCount := printValidationResults(os.Stderr, results); errorCount > 0 {
		return fmt.Errorf("validation failed: %d error(s) found. Resolve the issues above before generating Custom Resources", errorCount)
	}

	// Generation logic is implemented in the next stack.
	return fmt.Errorf("not yet implemented")
}

func parseMultiClusterNames(raw string) []string {
	var names []string
	for s := range strings.SplitSeq(raw, ",") {
		s = strings.TrimSpace(s)
		if s != "" {
			names = append(names, s)
		}
	}
	return names
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

// openOutput returns os.Stdout when path is empty, otherwise opens path for writing.
// The caller must close the returned file when it is not os.Stdout.
func openOutput(path string) (*os.File, error) {
	if path == "" {
		return os.Stdout, nil
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("failed to open output file %q: %w", path, err)
	}
	return f, nil
}

// userKey returns a unique key for a username+database pair.
func userKey(username, database string) string { return username + ":" + database }

// omUserKey mirrors userKey for automation config users.
func omUserKey(u om.MongoDBUser) string { return userKey(u.Username, u.Database) }
