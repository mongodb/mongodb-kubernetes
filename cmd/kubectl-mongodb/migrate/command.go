package migrate

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/authentication"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/secret"
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
	conn, kubeClient, err := prepareConnection(ctx)
	if err != nil {
		return err
	}
	ac, err := conn.ReadAutomationConfig()
	if err != nil {
		return fmt.Errorf("failed to read automation config: %w", err)
	}

	projectAgentConfigs, projectProcessConfigs, err := readProjectConfigs(conn)
	if err != nil {
		return err
	}

	processMap := ac.Deployment.ProcessMap()
	results, sourceProcess := ValidateMigration(ac, processMap, projectAgentConfigs, projectProcessConfigs)
	if errorCount := printValidationResults(os.Stderr, results); errorCount > 0 {
		return fmt.Errorf("validation failed: %d error(s) found. Resolve the issues above before generating Custom Resources.", errorCount)
	}

	opts := GenerateOptions{
		ReplicaSetNameOverride: replicaSetNameOverride,
		CredentialsSecretName:  secretName,
		ConfigMapName:          configMapName,
		ProjectAgentConfigs:    projectAgentConfigs,
		ProjectProcessConfigs:  projectProcessConfigs,
		ProcessMap:             processMap,
		Members:                ac.Deployment.GetReplicaSets()[0].Members(), // safe: ValidateMigration returns an error above when there are no replica sets
		SourceProcess:          sourceProcess,
	}

	if multiClusterNames != "" {
		opts.MultiClusterNames = parseMultiClusterNames(multiClusterNames)
		if len(opts.MultiClusterNames) == 0 {
			return fmt.Errorf("--multi-cluster-names was provided but contains no valid cluster names after trimming")
		}
	}

	stdinScanner := bufio.NewScanner(os.Stdin)

	if err := ensureTLS(&opts, stdinScanner); err != nil {
		return err
	}

	mongodbYAML, crName, err := GenerateMongoDBCR(ac, opts)
	if err != nil {
		return fmt.Errorf("failed to generate Custom Resource: %w", err)
	}

	userCRs, err := GenerateUserCRs(ac, crName)
	if err != nil {
		return fmt.Errorf("failed to generate user Custom Resources: %w", err)
	}

	ldapBindQuerySecret, ldapCAConfigMap, err := GenerateLdapResources(ac, namespace)
	if err != nil {
		return fmt.Errorf("failed to generate LDAP resources: %w", err)
	}

	if err := ensurePrometheus(ctx, ac, kubeClient, stdinScanner); err != nil {
		return err
	}

	if err := ensureUserSecrets(ctx, ac, userCRs, kubeClient, stdinScanner); err != nil {
		return err
	}

	out, err := openOutput(outputFile)
	if err != nil {
		return err
	}
	if f, ok := out.(*os.File); ok && f != os.Stdout {
		defer func() { _ = f.Close() }()
	}

	_, _ = fmt.Fprint(out, mongodbYAML)
	for _, u := range userCRs {
		_, _ = fmt.Fprintf(out, "---\n%s", u.YAML)
	}
	if ldapBindQuerySecret != "" {
		_, _ = fmt.Fprintf(out, "---\n%s", ldapBindQuerySecret)
	}
	if ldapCAConfigMap != "" {
		_, _ = fmt.Fprintf(out, "---\n%s", ldapCAConfigMap)
	}

	if outputFile != "" {
		fmt.Fprintf(os.Stderr, "CRs written to %s\n", outputFile)
	}

	return nil
}

// openOutput returns os.Stdout when path is empty, otherwise opens path for writing.
func openOutput(path string) (io.Writer, error) {
	if path == "" {
		return os.Stdout, nil
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open output file %q: %w", path, err)
	}
	return f, nil
}

func ensureTLS(opts *GenerateOptions, scanner *bufio.Scanner) error {
	if len(opts.Members) == 0 {
		return nil
	}
	tlsEnabled, err := isTLSEnabled(opts.ProcessMap, opts.Members)
	if err != nil {
		return fmt.Errorf("failed to detect TLS in automation config: %w", err)
	}
	if !tlsEnabled {
		return nil
	}
	prefix, err := promptCertsSecretPrefix(scanner)
	if err != nil {
		return err
	}
	opts.CertsSecretPrefix = prefix
	return nil
}

func promptCertsSecretPrefix(scanner *bufio.Scanner) (string, error) {
	fmt.Fprintf(os.Stderr, "Enter value for security.certsSecretPrefix (e.g. mdb): ")
	if !scanner.Scan() {
		return "", fmt.Errorf("failed to read certsSecretPrefix")
	}
	s := strings.TrimSpace(scanner.Text())
	if s == "" {
		return "", fmt.Errorf("security.certsSecretPrefix is required when TLS is enabled")
	}
	return s, nil
}

func ensurePrometheus(ctx context.Context, ac *om.AutomationConfig, kubeClient kubernetesClient.Client, scanner *bufio.Scanner) error {
	acProm := ac.Deployment.GetPrometheus()
	if acProm == nil || !acProm.Enabled || acProm.Username == "" {
		return nil
	}
	if kubeClient == nil {
		return fmt.Errorf("prometheus is enabled and requires a password secret, but no Kubernetes client is available. Ensure kubeconfig is configured")
	}
	fmt.Fprintf(os.Stderr, "Enter password for Prometheus user: ")
	if !scanner.Scan() {
		return fmt.Errorf("failed to read Prometheus password")
	}
	promPassword := strings.TrimSpace(scanner.Text())
	if promPassword == "" {
		return fmt.Errorf("Prometheus password cannot be empty")
	}
	sec := GeneratePasswordSecret(PrometheusPasswordSecretName, namespace, promPassword)
	if err := secret.CreateOrUpdate(ctx, kubeClient, sec); err != nil {
		return fmt.Errorf("failed to create Prometheus password secret: %w", err)
	}
	fmt.Fprintf(os.Stderr, "Created secret %q in namespace %q\n", PrometheusPasswordSecretName, namespace)
	return nil
}

// ensureUserSecrets prompts for each SCRAM user's password, validates it against OM, and creates the Kubernetes Secret.
func ensureUserSecrets(ctx context.Context, ac *om.AutomationConfig, userCRs []UserCROutput, kubeClient kubernetesClient.Client, scanner *bufio.Scanner) error {
	if len(userCRs) == 0 {
		return nil
	}

	for _, u := range userCRs {
		if !u.NeedsPassword {
			continue
		}
		if kubeClient == nil {
			return fmt.Errorf("user Custom Resources require password secrets, but no Kubernetes client is available. Ensure kubeconfig is configured")
		}

		fmt.Fprintf(os.Stderr, "Enter password for SCRAM user %q (db: %s): ", u.Username, u.Database)
		if !scanner.Scan() {
			return fmt.Errorf("failed to read password for user %q", u.Username)
		}
		password := strings.TrimSpace(scanner.Text())
		if password == "" {
			return fmt.Errorf("Password for user %q cannot be empty.", u.Username)
		}

		if err := validatePasswordAgainstOM(u.Username, u.Database, password, ac); err != nil {
			return err
		}

		sec := GeneratePasswordSecret(u.PasswordSecret, namespace, password)
		if err := secret.CreateOrUpdate(ctx, kubeClient, sec); err != nil {
			return fmt.Errorf("failed to create password secret %q for user %q: %w", u.PasswordSecret, u.Username, err)
		}
		fmt.Fprintf(os.Stderr, "Created secret %q in namespace %q\n", u.PasswordSecret, namespace)
	}
	return nil
}

// validatePasswordAgainstOM errors when the password doesn't match the SCRAM hashes in OM.
func validatePasswordAgainstOM(username, database, password string, ac *om.AutomationConfig) error {
	_, acUser := ac.Auth.GetUser(username, database)
	user := &om.MongoDBUser{Username: username, Database: database}
	changed, err := authentication.IsPasswordChanged(user, password, acUser)
	if err != nil {
		return fmt.Errorf("failed to validate password for user %q: %w", username, err)
	}
	if changed {
		return fmt.Errorf("password for user %q does not match the existing credentials in Ops Manager. "+
			"Please enter the correct password that the user currently has in OM.", username)
	}
	return nil
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
		_, _ = fmt.Fprintf(w, "[%s] %s\n\n", r.Severity, r.Message)
		if r.Severity == SeverityError {
			errorCount++
		}
	}
	return errorCount
}
