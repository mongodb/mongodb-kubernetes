package migratetomck

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"

	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/authentication"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

const defaultNamespace = "default"

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
	ac, err := conn.ReadAutomationConfig()
	if err != nil {
		return fmt.Errorf("failed to read automation config: %w", err)
	}
	projectConfigs, err := readProjectConfigs(conn)
	if err != nil {
		return err
	}

	processMap := ac.Deployment.ProcessMap()
	results, sourceProcess := ValidateMigration(ac, processMap, projectConfigs)
	if errorCount := printValidationResults(os.Stderr, results); errorCount > 0 {
		return fmt.Errorf("validation failed: %d error(s) found. Resolve the issues above before generating Custom Resources", errorCount)
	}

	opts := GenerateOptions{
		ReplicaSetNameOverride: replicaSetNameOverride,
		CredentialsSecretName:  secretName,
		ConfigMapName:          configMapName,
		Namespace:              namespace,
		ProjectConfigs:         projectConfigs,
		SourceProcess:          sourceProcess,
	}

	if multiClusterNames != "" {
		opts.MultiClusterNames = parseMultiClusterNames(multiClusterNames)
		if len(opts.MultiClusterNames) == 0 {
			return fmt.Errorf("--multi-cluster-names was provided but contains no valid cluster names after trimming")
		}
	}

	stdinScanner := bufio.NewScanner(os.Stdin)

	if err := ensureTLS(ac, &opts, stdinScanner); err != nil {
		return err
	}
	if err := collectPrometheusPassword(ac, &opts, stdinScanner); err != nil {
		return err
	}
	if err := collectUserPasswords(ac, &opts, stdinScanner); err != nil {
		return err
	}

	resources, err := generateMigrationResources(ac, opts)
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

	if _, err := fmt.Fprint(out, resources); err != nil {
		return fmt.Errorf("failed to write output: %w", err)
	}
	if outputFile != "" {
		fmt.Fprintf(os.Stderr, "CRs written to %s\n", outputFile)
	}
	return nil
}

// generateMigrationResources generates all CRs and supporting Secrets/ConfigMaps as a single YAML output.
// Nothing is applied to the cluster — the customer owns the apply step.
func generateMigrationResources(ac *om.AutomationConfig, opts GenerateOptions) (string, error) {
	mongodbYAML, crName, err := GenerateMongoDBCR(ac, opts)
	if err != nil {
		return "", fmt.Errorf("failed to generate Custom Resource: %w", err)
	}

	userCRs, err := GenerateUserCRs(ac, crName, opts.Namespace, opts.UserPasswords)
	if err != nil {
		return "", fmt.Errorf("failed to generate user Custom Resources: %w", err)
	}

	var sb strings.Builder
	sb.WriteString(mongodbYAML)

	for _, u := range userCRs {
		sb.WriteString("---\n")
		sb.WriteString(u.MongoDBUserYAML)
		if u.PasswordSecretYAML != "" {
			sb.WriteString("---\n")
			sb.WriteString(u.PasswordSecretYAML)
		}
	}

	extra := append(generateLdapResources(ac, opts), generatePrometheusResources(ac, opts)...)
	if err := appendMarshaledResources(&sb, extra); err != nil {
		return "", err
	}

	return sb.String(), nil
}

func appendMarshaledResources(sb *strings.Builder, resources []any) error {
	for _, r := range resources {
		y, err := marshalCRToYAML(r)
		if err != nil {
			return fmt.Errorf("failed to marshal %T: %w", r, err)
		}
		sb.WriteString("---\n")
		sb.WriteString(y)
	}
	return nil
}

func generateLdapResources(ac *om.AutomationConfig, opts GenerateOptions) []any {
	if ac.Ldap == nil {
		return nil
	}
	var resources []any
	if ac.Ldap.BindQueryPassword != "" {
		resources = append(resources, GeneratePasswordSecret(LdapBindQuerySecretName, opts.Namespace, ac.Ldap.BindQueryPassword))
	}
	if ac.Ldap.CaFileContents != "" {
		resources = append(resources, buildLdapCAConfigMap(opts.Namespace, ac.Ldap.CaFileContents))
	}
	return resources
}

func generatePrometheusResources(ac *om.AutomationConfig, opts GenerateOptions) []any {
	acProm := ac.Deployment.GetPrometheus()
	if opts.PrometheusPassword == "" || acProm == nil || !acProm.Enabled || acProm.Username == "" {
		return nil
	}
	return []any{GeneratePasswordSecret(PrometheusPasswordSecretName, opts.Namespace, opts.PrometheusPassword)}
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

// promptLine prints prompt to stderr and returns the trimmed input line, or an error on EOF/scan failure.
func promptLine(scanner *bufio.Scanner, prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return "", err
		}
		return "", fmt.Errorf("input cancelled")
	}
	return strings.TrimSpace(scanner.Text()), nil
}

func ensureTLS(ac *om.AutomationConfig, opts *GenerateOptions, scanner *bufio.Scanner) error {
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
	prefix, err := promptLine(scanner, "Enter value for security.certsSecretPrefix (e.g. mdb): ")
	if err != nil {
		return fmt.Errorf("failed to read certsSecretPrefix: %w", err)
	}
	if prefix == "" {
		return fmt.Errorf("security.certsSecretPrefix is required when TLS is enabled")
	}
	if len(k8svalidation.IsDNS1123Subdomain(prefix)) > 0 {
		return fmt.Errorf("security.certsSecretPrefix %q is not a valid Kubernetes resource name", prefix)
	}
	opts.CertsSecretPrefix = prefix
	return nil
}

func collectPrometheusPassword(ac *om.AutomationConfig, opts *GenerateOptions, scanner *bufio.Scanner) error {
	acProm := ac.Deployment.GetPrometheus()
	if acProm == nil || !acProm.Enabled || acProm.Username == "" {
		return nil
	}
	promPassword, err := promptLine(scanner, "Enter password for Prometheus user: ")
	if err != nil {
		return fmt.Errorf("failed to read Prometheus password: %w", err)
	}
	if promPassword == "" {
		return fmt.Errorf("prometheus password cannot be empty")
	}
	opts.PrometheusPassword = promPassword
	return nil
}

func collectUserPasswords(ac *om.AutomationConfig, opts *GenerateOptions, scanner *bufio.Scanner) error {
	if ac.Auth == nil || len(ac.Auth.Users) == 0 {
		return nil
	}
	opts.UserPasswords = make(map[string]string)
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
		password, err := promptLine(scanner, fmt.Sprintf("Enter password for SCRAM user %q (db: %s): ", user.Username, user.Database))
		if err != nil {
			return fmt.Errorf("failed to read password for user %q: %w", user.Username, err)
		}
		if password == "" {
			return fmt.Errorf("password for user %q cannot be empty", user.Username)
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
