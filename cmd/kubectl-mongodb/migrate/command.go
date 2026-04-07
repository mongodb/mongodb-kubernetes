package migrate

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/authentication"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

const defaultNamespace = "default"

var (
	configMapName          string
	secretName             string
	namespace              string
	multiClusterNames      string
	outputFile             string
	replicaSetNameOverride string
	dryRun                 bool
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
	MigrateCmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print supportive resources (secrets, configmaps) to output instead of applying them to the cluster")
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
		return fmt.Errorf("validation failed: %d error(s) found. Resolve the issues above before generating Custom Resources", errorCount)
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
		DryRun:                 dryRun,
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
	if err := collectPrometheusPassword(ac, &opts, stdinScanner); err != nil {
		return err
	}
	if err := collectUserPasswords(ac, &opts, stdinScanner); err != nil {
		return err
	}

	mongodbYAML, userCRs, clusterYAML, err := generateAll(ctx, ac, opts, kubeClient)
	if err != nil {
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
		_, _ = fmt.Fprint(out, "---\n")
		_, _ = fmt.Fprint(out, u.YAML)
	}
	_, _ = fmt.Fprint(out, clusterYAML)
	if outputFile != "" {
		fmt.Fprintf(os.Stderr, "CRs written to %s\n", outputFile)
	}
	return nil
}

// generateAll generates all CRs and cluster resources, returning each piece separately.
func generateAll(ctx context.Context, ac *om.AutomationConfig, opts GenerateOptions, kubeClient kubernetesClient.Client) (mongodbYAML string, userCRs []UserCROutput, clusterYAML string, err error) {
	mongodbYAML, crName, err := GenerateMongoDBCR(ac, opts)
	if err != nil {
		return "", nil, "", fmt.Errorf("failed to generate Custom Resource: %w", err)
	}

	userCRs, err = GenerateUserCRs(ac, crName)
	if err != nil {
		return "", nil, "", fmt.Errorf("failed to generate user Custom Resources: %w", err)
	}

	clusterYAML, err = applyClusterResources(ctx, ac, opts, userCRs, kubeClient)
	if err != nil {
		return "", nil, "", err
	}

	return mongodbYAML, userCRs, clusterYAML, nil
}

// applyClusterResources applies secrets/configmaps to the cluster, or returns their YAML when dry-run.
func applyClusterResources(ctx context.Context, ac *om.AutomationConfig, opts GenerateOptions, userCRs []UserCROutput, kubeClient kubernetesClient.Client) (string, error) {
	var resources []any

	if ac.Ldap != nil {
		if ac.Ldap.BindQueryPassword != "" {
			resources = append(resources, GeneratePasswordSecret(LdapBindQuerySecretName, namespace, ac.Ldap.BindQueryPassword))
		}
		if ac.Ldap.CaFileContents != "" {
			resources = append(resources, buildLdapCAConfigMap(namespace, ac.Ldap.CaFileContents))
		}
	}

	acProm := ac.Deployment.GetPrometheus()
	if opts.PrometheusPassword != "" && acProm != nil && acProm.Enabled && acProm.Username != "" {
		resources = append(resources, GeneratePasswordSecret(PrometheusPasswordSecretName, namespace, opts.PrometheusPassword))
	}

	for _, u := range userCRs {
		if !u.NeedsPassword {
			continue
		}
		password, ok := opts.UserPasswords[userKey(u.Username, u.Database)]
		if !ok {
			continue
		}
		resources = append(resources, GeneratePasswordSecret(u.PasswordSecret, namespace, password))
	}

	if !opts.DryRun && kubeClient == nil && len(resources) > 0 {
		return "", fmt.Errorf("cluster resources require a Kubernetes client, but none is available. Ensure kubeconfig is configured")
	}

	var sb strings.Builder
	for _, r := range resources {
		if opts.DryRun {
			y, err := marshalCRToYAML(r)
			if err != nil {
				return "", fmt.Errorf("failed to marshal %T: %w", r, err)
			}
			sb.WriteString("---\n")
			sb.WriteString(y)
		} else {
			switch v := r.(type) {
			case corev1.Secret:
				if err := kubeClient.CreateSecret(ctx, v); k8serrors.IsAlreadyExists(err) {
					fmt.Fprintf(os.Stderr, "Secret %q already exists in namespace %q, skipping\n", v.Name, namespace)
				} else if err != nil {
					return "", fmt.Errorf("failed to create secret %q: %w", v.Name, err)
				} else {
					fmt.Fprintf(os.Stderr, "Created secret %q in namespace %q\n", v.Name, namespace)
				}
			case corev1.ConfigMap:
				if err := kubeClient.CreateConfigMap(ctx, v); k8serrors.IsAlreadyExists(err) {
					fmt.Fprintf(os.Stderr, "ConfigMap %q already exists in namespace %q, skipping\n", v.Name, namespace)
				} else if err != nil {
					return "", fmt.Errorf("failed to create configmap %q: %w", v.Name, err)
				} else {
					fmt.Fprintf(os.Stderr, "Created configmap %q in namespace %q\n", v.Name, namespace)
				}
			}
		}
	}
	return sb.String(), nil
}

// userKey returns the map key used to associate a user with their password.
func userKey(username, database string) string { return username + ":" + database }

// openOutput returns os.Stdout when path is empty, otherwise opens path for writing.
func openOutput(path string) (io.Writer, error) {
	if path == "" {
		return os.Stdout, nil
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
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

// collectPrometheusPassword prompts for the Prometheus user password and stores it in opts.
func collectPrometheusPassword(ac *om.AutomationConfig, opts *GenerateOptions, scanner *bufio.Scanner) error {
	acProm := ac.Deployment.GetPrometheus()
	if acProm == nil || !acProm.Enabled || acProm.Username == "" {
		return nil
	}
	fmt.Fprintf(os.Stderr, "Enter password for Prometheus user: ")
	if !scanner.Scan() {
		return fmt.Errorf("failed to read Prometheus password")
	}
	promPassword := strings.TrimSpace(scanner.Text())
	if promPassword == "" {
		return fmt.Errorf("prometheus password cannot be empty")
	}
	opts.PrometheusPassword = promPassword
	return nil
}

// collectUserPasswords prompts for each SCRAM user's password, validates it against OM, and stores it in opts.
func collectUserPasswords(ac *om.AutomationConfig, opts *GenerateOptions, scanner *bufio.Scanner) error {
	if ac.Auth == nil || len(ac.Auth.Users) == 0 {
		return nil
	}
	for _, user := range ac.Auth.Users {
		if user == nil || user.Username == "" {
			continue
		}
		if user.Username == ac.Auth.AutoUser && user.Database == util.DefaultUserDatabase {
			continue
		}
		if user.Database == externalDatabase {
			continue
		}
		fmt.Fprintf(os.Stderr, "Enter password for SCRAM user %q (db: %s): ", user.Username, user.Database)
		if !scanner.Scan() {
			return fmt.Errorf("failed to read password for user %q", user.Username)
		}
		password := strings.TrimSpace(scanner.Text())
		if password == "" {
			return fmt.Errorf("password for user %q cannot be empty", user.Username)
		}
		if err := validatePasswordAgainstOM(user.Username, user.Database, password, ac); err != nil {
			return err
		}
		if opts.UserPasswords == nil {
			opts.UserPasswords = map[string]string{}
		}
		opts.UserPasswords[userKey(user.Username, user.Database)] = password
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
		return fmt.Errorf("password for user %q does not match the existing credentials in Ops Manager", username)
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
		if r.Severity == SeverityError {
			errorCount++
		}
		_, _ = fmt.Fprintf(w, "[%s] %s\n\n", r.Severity, r.Message)
	}
	return errorCount
}
