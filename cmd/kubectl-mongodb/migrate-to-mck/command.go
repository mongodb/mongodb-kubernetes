package migratetomck

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	k8svalidation "k8s.io/apimachinery/pkg/util/validation"

	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/authentication"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

const defaultNamespace = "default"

// promptOutput is the writer used for interactive prompts. Override in tests to suppress stderr noise.
var promptOutput io.Writer = os.Stderr

type cliFlags struct {
	configMapName          string
	secretName             string
	namespace              string
	multiClusterNames      string
	outputFile             string
	replicaSetNameOverride string
	usersSecretsFile       string
	certsSecretPrefix      string
	prometheusSecretName   string
}

var flags cliFlags

var MigrateCmd = &cobra.Command{
	Use:   "migrate-to-mck",
	Short: "Migrate MongoDB deployments to Kubernetes",
	Long: `Generates Kubernetes Custom Resources from an Ops Manager/Cloud Manager automation
config for migrating existing deployments to the operator.

Use one of the subcommands to generate specific resource types:
  mongodb  Generate a MongoDB CR
  users    Generate MongoDBUser CRs`,
}

func init() {
	MigrateCmd.Flags().StringVar(&flags.configMapName, "config-map-name", "", "Name of the ConfigMap containing the OM connection details (baseUrl, orgId, projectName) (required)")
	MigrateCmd.Flags().StringVar(&flags.secretName, "secret-name", "", "Name of the Secret containing the OM API credentials (publicKey, privateKey) (required)")
	MigrateCmd.Flags().StringVar(&flags.namespace, "namespace", defaultNamespace, "Namespace of the ConfigMap and Secret")
	MigrateCmd.Flags().StringVar(&flags.multiClusterNames, "multi-cluster-names", "", "Comma-separated list of target cluster names (e.g., east1,west1). Generates a MongoDBMultiCluster CR")
	MigrateCmd.Flags().StringVarP(&flags.outputFile, "output", "o", "", "Write generated CRs to this file instead of stdout")
	MigrateCmd.Flags().StringVar(&flags.replicaSetNameOverride, "replicaset-name-override", "", "Kubernetes resource name for the generated CR; required when the replica set name is not a valid Kubernetes name (sets spec.replicaSetNameOverride automatically)")
	MigrateCmd.Flags().StringVar(&flags.usersSecretsFile, "users-secrets-file", "", "CSV file mapping 'username:database,secret-name' for SCRAM users; when provided, customer-owned Secrets are referenced instead of generated and interactive prompts for user passwords are suppressed")
	MigrateCmd.Flags().StringVar(&flags.certsSecretPrefix, "certs-secret-prefix", "", "Value for spec.security.certsSecretPrefix; required when TLS is enabled and suppresses the interactive prompt")
	MigrateCmd.Flags().StringVar(&flags.prometheusSecretName, "prometheus-secret-name", "", "Name of a pre-created Kubernetes Secret containing the Prometheus password (key: \"password\"); suppresses the interactive prompt")
	_ = MigrateCmd.MarkFlagRequired("config-map-name")
	_ = MigrateCmd.MarkFlagRequired("secret-name")
}

func runGenerate(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()

	conn, kubeClient, err := prepareConnection(ctx, flags.namespace, flags.configMapName, flags.secretName)
	if err != nil {
		return err
	}

	ac, projectConfigs, sourceProcess, err := fetchAndValidate(conn)
	if err != nil {
		return err
	}

	opts, err := buildOptions(ctx, kubeClient, ac, projectConfigs, sourceProcess, os.Stdin, flags)
	if err != nil {
		return err
	}

	resources, err := generateMigrationResources(ac, opts)
	if err != nil {
		return err
	}

	return writeOutput(resources, flags.outputFile)
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

// buildOptions converts CLI flags to a GenerateOptions, prompting for any values not supplied
// as flags (certsSecretPrefix when TLS is enabled, Prometheus password, user passwords).
func buildOptions(ctx context.Context, kubeClient kubernetesClient.Client, ac *om.AutomationConfig, projectConfigs *ProjectConfigs, sourceProcess *om.Process, stdin io.Reader, flags cliFlags) (GenerateOptions, error) {
	opts := GenerateOptions{
		ReplicaSetNameOverride: flags.replicaSetNameOverride,
		CredentialsSecretName:  flags.secretName,
		ConfigMapName:          flags.configMapName,
		Namespace:              flags.namespace,
		ProjectConfigs:         projectConfigs,
		SourceProcess:          sourceProcess,
	}

	if flags.multiClusterNames != "" {
		opts.MultiClusterNames = parseMultiClusterNames(flags.multiClusterNames)
		if len(opts.MultiClusterNames) == 0 {
			return GenerateOptions{}, fmt.Errorf("--multi-cluster-names was provided but contains no valid cluster names after trimming")
		}
	}

	scanner := bufio.NewScanner(stdin)

	if err := ensureTLS(ac, &opts, scanner, flags.certsSecretPrefix); err != nil {
		return GenerateOptions{}, err
	}
	if err := collectPrometheusCreds(ctx, kubeClient, ac, &opts, scanner, flags.prometheusSecretName); err != nil {
		return GenerateOptions{}, err
	}
	if err := collectUserCreds(ctx, kubeClient, ac, &opts, scanner, flags.usersSecretsFile); err != nil {
		return GenerateOptions{}, err
	}
	return opts, nil
}

func writeOutput(resources, outputFile string) error {
	if outputFile == "" {
		_, err := fmt.Fprint(os.Stdout, resources)
		return err
	}
	f, err := os.OpenFile(outputFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("failed to open output file %q: %w", outputFile, err)
	}
	defer func() { _ = f.Close() }()
	if _, err := fmt.Fprint(f, resources); err != nil {
		return fmt.Errorf("failed to write output: %w", err)
	}
	fmt.Fprintf(os.Stderr, "CRs written to %s\n", outputFile)
	return nil
}

func userKey(username, database string) string { return username + ":" + database }

func promptLine(scanner *bufio.Scanner, prompt string) (string, error) {
	_, _ = fmt.Fprint(promptOutput, prompt)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return "", err
		}
		return "", fmt.Errorf("input cancelled")
	}
	return strings.TrimSpace(scanner.Text()), nil
}

func ensureTLS(ac *om.AutomationConfig, opts *GenerateOptions, scanner *bufio.Scanner, certsSecretPrefix string) error {
	rss := ac.Deployment.GetReplicaSets()
	if len(rss) == 0 {
		return nil
	}
	tlsEnabled, err := isTLSEnabled(ac.Deployment.ProcessMap(), rss[0].Members())
	if err != nil {
		return fmt.Errorf("failed to detect TLS in automation config: %w", err)
	}
	if !tlsEnabled {
		return nil
	}
	prefix := certsSecretPrefix
	if prefix == "" {
		p, err := promptLine(scanner, "Enter value for security.certsSecretPrefix (e.g. mdb): ")
		if err != nil {
			return fmt.Errorf("failed to read certsSecretPrefix: %w", err)
		}
		prefix = p
	}
	if prefix == "" {
		return fmt.Errorf("security.certsSecretPrefix is required when TLS is enabled; use --certs-secret-prefix or enter it interactively")
	}
	if len(k8svalidation.IsDNS1123Subdomain(prefix)) > 0 {
		return fmt.Errorf("security.certsSecretPrefix %q is not a valid Kubernetes resource name", prefix)
	}
	opts.CertsSecretPrefix = prefix
	return nil
}

// collectPrometheusCreds handles the Prometheus password independently of the user-secrets mode.
// If --prometheus-secret-name is provided the Secret is validated in Kubernetes and referenced
// in the CR (no YAML generated). Otherwise the password is collected interactively.
func collectPrometheusCreds(ctx context.Context, kubeClient kubernetesClient.Client, ac *om.AutomationConfig, opts *GenerateOptions, scanner *bufio.Scanner, prometheusSecretName string) error {
	acProm := ac.Deployment.GetPrometheus()
	if acProm == nil || !acProm.Enabled || acProm.Username == "" {
		return nil
	}
	if prometheusSecretName != "" {
		secret, err := kubeClient.GetSecret(ctx, kube.ObjectKey(opts.Namespace, prometheusSecretName))
		if err != nil {
			return fmt.Errorf("--prometheus-secret-name: secret %q not found in namespace %q: %w", prometheusSecretName, opts.Namespace, err)
		}
		if _, ok := secret.Data[passwordSecretDataKey]; !ok {
			return fmt.Errorf("--prometheus-secret-name: secret %q does not contain key \"password\"", prometheusSecretName)
		}
		opts.PrometheusSecretName = prometheusSecretName
		return nil
	}
	password, err := promptLine(scanner, "Enter password for Prometheus user: ")
	if err != nil {
		return fmt.Errorf("failed to read Prometheus password: %w", err)
	}
	if password == "" {
		return fmt.Errorf("prometheus password cannot be empty")
	}
	opts.PrometheusPassword = password
	return nil
}

// scramUsers returns the non-agent, non-external SCRAM users from the automation config.
func scramUsers(ac *om.AutomationConfig) []*om.MongoDBUser {
	if ac.Auth == nil {
		return nil
	}
	var users []*om.MongoDBUser
	for _, u := range ac.Auth.Users {
		if u == nil || u.Username == "" ||
			(u.Username == ac.Auth.AutoUser && u.Database == util.DefaultUserDatabase) ||
			u.Database == externalDatabase {
			continue
		}
		users = append(users, u)
	}
	return users
}

func collectUserCreds(ctx context.Context, kubeClient kubernetesClient.Client, ac *om.AutomationConfig, opts *GenerateOptions, scanner *bufio.Scanner, usersSecretsFile string) error {
	if usersSecretsFile != "" {
		fileMapping, err := parseUsersSecretsFile(usersSecretsFile)
		if err != nil {
			return fmt.Errorf("failed to parse --users-secrets-file: %w", err)
		}
		return collectExistingUserSecrets(ctx, kubeClient, ac, opts, fileMapping)
	}
	return collectUserPasswords(ac, opts, scanner)
}

func collectUserPasswords(ac *om.AutomationConfig, opts *GenerateOptions, scanner *bufio.Scanner) error {
	opts.UserPasswords = make(map[string]string)
	for _, user := range scramUsers(ac) {
		password, err := promptLine(scanner, fmt.Sprintf("Enter password for SCRAM user %q (db: %s) [Enter to skip]: ", user.Username, user.Database))
		if err != nil {
			return fmt.Errorf("failed to read password for user %q: %w", user.Username, err)
		}
		if password == "" {
			continue
		}
		if err := validatePasswordAgainstOM(user.Username, user.Database, password, ac); err != nil {
			return err
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

// collectExistingUserSecrets populates opts.ExistingUserSecrets for each non-external SCRAM user.
// Users absent from fileMapping are skipped with a warning; present users are validated against
// the Kubernetes Secret and their SCRAM hashes in the automation config.
func collectExistingUserSecrets(ctx context.Context, kubeClient kubernetesClient.Client, ac *om.AutomationConfig, opts *GenerateOptions, fileMapping map[string]string) error {
	opts.ExistingUserSecrets = make(map[string]string)
	for _, user := range scramUsers(ac) {
		key := userKey(user.Username, user.Database)
		secretName, ok := fileMapping[key]
		if !ok {
			continue
		}
		if err := validateUserSecret(ctx, kubeClient, user, secretName, ac, opts.Namespace); err != nil {
			return err
		}
		opts.ExistingUserSecrets[key] = secretName
	}
	return nil
}

// validateUserSecret reads the named Secret from Kubernetes and validates its password against
// the SCRAM hashes stored in the automation config for the given user.
func validateUserSecret(ctx context.Context, kubeClient kubernetesClient.Client, user *om.MongoDBUser, secretName string, ac *om.AutomationConfig, namespace string) error {
	secret, err := kubeClient.GetSecret(ctx, kube.ObjectKey(namespace, secretName))
	if err != nil {
		return fmt.Errorf("secret %q not found in namespace %q (user %q): %w", secretName, namespace, user.Username, err)
	}
	passwordBytes, ok := secret.Data[passwordSecretDataKey]
	if !ok {
		return fmt.Errorf("secret %q does not contain key \"password\" (required for user %q)", secretName, user.Username)
	}
	return validatePasswordAgainstOM(user.Username, user.Database, string(passwordBytes), ac)
}

// parseUsersSecretsFile reads a CSV file mapping "username:database,secret-name" lines to a map.
// Blank lines and lines beginning with '#' are ignored.
func parseUsersSecretsFile(path string) (map[string]string, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %w", err)
	}
	defer func() { _ = f.Close() }()

	result := make(map[string]string)
	sc := bufio.NewScanner(f)
	for lineNum := 1; sc.Scan(); lineNum++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		userDB, sName, ok := strings.Cut(line, ",")
		userDB, sName = strings.TrimSpace(userDB), strings.TrimSpace(sName)
		if !ok || userDB == "" || sName == "" {
			return nil, fmt.Errorf("line %d: expected \"username:database,secret-name\", got %q", lineNum, line)
		}
		if !strings.Contains(userDB, ":") {
			return nil, fmt.Errorf("line %d: first field %q is missing the database part; expected \"username:database\"", lineNum, userDB)
		}
		if errs := k8svalidation.IsDNS1123Subdomain(sName); len(errs) > 0 {
			return nil, fmt.Errorf("line %d: secret name %q is not a valid Kubernetes name: %s", lineNum, sName, errs[0])
		}
		result[userDB] = sName
	}
	return result, sc.Err()
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
