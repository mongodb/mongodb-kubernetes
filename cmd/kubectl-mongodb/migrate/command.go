package migrate

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/authentication"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
)

const defaultNamespace = "default"

var (
	configMapName     string
	secretName        string
	namespace         string
	multiClusterNames string
)

var MigrateCmd = &cobra.Command{
	Use:   "migrate",
	Short: "Migrate MongoDB deployments to Kubernetes",
	Long: `Generates MongoDB/MongoDBUser Kubernetes CRs from an Ops Manager automation
config for migrating existing deployments to the operator. The automation
config is validated automatically before generation; if any blockers are
found the command fails without producing output.

Requires a ConfigMap (baseUrl, orgId, projectName) and a Secret (publicKey,
privateKey) in the same format used by the operator.`,
}

func init() {
	MigrateCmd.PersistentFlags().StringVar(&configMapName, "config-map-name", "", "Name of the ConfigMap containing the OM URL and project ID (required)")
	MigrateCmd.PersistentFlags().StringVar(&secretName, "secret-name", "", "Name of the Secret containing the OM API credentials (required)")
	MigrateCmd.PersistentFlags().StringVar(&namespace, "namespace", defaultNamespace, "Namespace of the ConfigMap and Secret")
	MigrateCmd.PersistentFlags().StringVar(&multiClusterNames, "multi-cluster-names", "", "Comma-separated list of target cluster names (e.g., east1,west1); generates a MongoDBMultiCluster CR")
	_ = MigrateCmd.MarkPersistentFlagRequired("config-map-name")
	_ = MigrateCmd.MarkPersistentFlagRequired("secret-name")

	MigrateCmd.AddCommand(generateCmd)
}

var generateCmd = &cobra.Command{
	Use:   "generate",
	Short: "Validate automation config and generate MongoDB/MongoDBUser CRs",
	RunE:  runGenerate,
}

func runGenerate(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	conn, kubeClient, err := prepareConnection(ctx)
	if err != nil {
		return err
	}
	ac, err := conn.ReadAutomationConfig()
	if err != nil {
		return fmt.Errorf("error reading automation config: %w", err)
	}

	projectAgentConfigs, projectProcessConfigs, err := readProjectConfigs(conn)
	if err != nil {
		return err
	}

	results := ValidateMigration(ac, projectAgentConfigs, projectProcessConfigs)
	if errorCount := printValidationResults(results); errorCount > 0 {
		return fmt.Errorf("validation failed: %d error(s) found; fix the issues above before generating CRs", errorCount)
	}

	opts := GenerateOptions{
		CredentialsSecretName: secretName,
		ConfigMapName:         configMapName,
		AgentConfigs:          projectAgentConfigs,
		ProcessConfigs:        projectProcessConfigs,
	}

	if multiClusterNames != "" {
		opts.MultiClusterNames = parseMultiClusterNames(multiClusterNames)
		if len(opts.MultiClusterNames) == 0 {
			return fmt.Errorf("--multi-cluster-names was provided but contains no valid cluster names after trimming")
		}
	}

	yamlOut, resourceName, err := GenerateMongoDBCR(ac, opts)
	if err != nil {
		return fmt.Errorf("error generating CR: %w", err)
	}
	fmt.Print(yamlOut)

	if err := ensurePrometheus(ctx, ac, kubeClient); err != nil {
		return err
	}

	if err := ensureUsers(ctx, ac, resourceName, kubeClient); err != nil {
		return err
	}

	return nil
}

func ensurePrometheus(ctx context.Context, ac *om.AutomationConfig, kubeClient kubernetesClient.Client) error {
	if !IsPrometheusEnabled(ac.Deployment) {
		return nil
	}
	if kubeClient == nil {
		return fmt.Errorf("prometheus is enabled and requires a password secret but no Kubernetes client is available; check your kubeconfig")
	}
	scanner := bufio.NewScanner(os.Stdin)
	fmt.Fprintf(os.Stderr, "Enter password for Prometheus user: ")
	if !scanner.Scan() {
		return fmt.Errorf("error reading Prometheus password")
	}
	promPassword := strings.TrimSpace(scanner.Text())
	if promPassword == "" {
		return fmt.Errorf("Prometheus password cannot be empty")
	}
	sec := GeneratePasswordSecret(PrometheusPasswordSecretName, namespace, promPassword)
	if err := kubeClient.CreateSecret(ctx, sec); err != nil {
		return fmt.Errorf("error creating Prometheus password secret: %w", err)
	}
	fmt.Fprintf(os.Stderr, "Created secret %q in namespace %q\n", PrometheusPasswordSecretName, namespace)
	return nil
}

func ensureUsers(ctx context.Context, ac *om.AutomationConfig, resourceName string, kubeClient kubernetesClient.Client) error {
	userCRs, err := GenerateUserCRs(ac, resourceName)
	if err != nil {
		return fmt.Errorf("error generating user CRs: %w", err)
	}
	if len(userCRs) == 0 {
		return nil
	}

	needsSecrets := false
	for _, u := range userCRs {
		if u.NeedsPassword {
			needsSecrets = true
			break
		}
	}
	if needsSecrets && kubeClient == nil {
		return fmt.Errorf("user CRs require password secrets but no Kubernetes client is available; check your kubeconfig")
	}

	scanner := bufio.NewScanner(os.Stdin)
	for _, u := range userCRs {
		if u.NeedsPassword && kubeClient != nil {
			fmt.Fprintf(os.Stderr, "Enter password for SCRAM user %q (db: %s): ", u.Username, u.Database)
			if !scanner.Scan() {
				return fmt.Errorf("error reading password for user %q", u.Username)
			}
			password := strings.TrimSpace(scanner.Text())
			if password == "" {
				return fmt.Errorf("password for user %q cannot be empty", u.Username)
			}

			user := &om.MongoDBUser{Username: u.Username, Database: u.Database}
			if err := authentication.ConfigureScramCredentials(user, password, ac); err != nil {
				return fmt.Errorf("error validating password for user %q: %w", u.Username, err)
			}

			sec := GeneratePasswordSecret(u.PasswordSecret, namespace, password)
			if err := kubeClient.CreateSecret(ctx, sec); err != nil {
				return fmt.Errorf("error creating password secret %q for user %q: %w", u.PasswordSecret, u.Username, err)
			}
			fmt.Fprintf(os.Stderr, "Created secret %q in namespace %q\n", u.PasswordSecret, namespace)
		}

		fmt.Printf("---\n%s", u.YAML)
	}
	return nil
}

func parseMultiClusterNames(raw string) []string {
	var names []string
	for _, s := range strings.Split(raw, ",") {
		s = strings.TrimSpace(s)
		if s != "" {
			names = append(names, s)
		}
	}
	return names
}

func printValidationResults(results []ValidationResult) int {
	errorCount := 0
	for _, r := range results {
		fmt.Fprintf(os.Stderr, "[%s] %s\n\n", r.Severity, r.Message)
		if r.Severity == SeverityError {
			errorCount++
		}
	}
	return errorCount
}
