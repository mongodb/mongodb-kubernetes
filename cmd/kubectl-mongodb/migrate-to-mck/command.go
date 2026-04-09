package migratetomck

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

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

	_, _, _, err = fetchAndValidate(conn)
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

	_, _ = fmt.Fprint(out, resources)
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

	userCRs, err := GenerateUserCRs(ac, crName, opts.UserPasswords)
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

	ldapResources, err := generateLdapResources(ac)
	if err != nil {
		return "", err
	}
	for _, r := range ldapResources {
		y, err := marshalCRToYAML(r)
		if err != nil {
			return "", fmt.Errorf("failed to marshal %T: %w", r, err)
		}
		sb.WriteString("---\n")
		sb.WriteString(y)
	}

	prometheusResources, err := generatePrometheusResources(ac, opts)
	if err != nil {
		return "", err
	}
	for _, r := range prometheusResources {
		y, err := marshalCRToYAML(r)
		if err != nil {
			return "", fmt.Errorf("failed to marshal %T: %w", r, err)
		}
		sb.WriteString("---\n")
		sb.WriteString(y)
	}

	return sb.String(), nil
}

func generateLdapResources(ac *om.AutomationConfig) ([]any, error) {
	if ac.Ldap == nil {
		return nil, nil
	}
	var resources []any
	if ac.Ldap.BindQueryPassword != "" {
		resources = append(resources, GeneratePasswordSecret(LdapBindQuerySecretName, namespace, ac.Ldap.BindQueryPassword))
	}
	if ac.Ldap.CaFileContents != "" {
		resources = append(resources, buildLdapCAConfigMap(namespace, ac.Ldap.CaFileContents))
	}
	return resources, nil
}

func generatePrometheusResources(ac *om.AutomationConfig, opts GenerateOptions) ([]any, error) {
	acProm := ac.Deployment.GetPrometheus()
	if opts.PrometheusPassword == "" || acProm == nil || !acProm.Enabled || acProm.Username == "" {
		return nil, nil
	}
	return []any{GeneratePasswordSecret(PrometheusPasswordSecretName, namespace, opts.PrometheusPassword)}, nil
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

// omUserKey mirrors userKey for automation config users.
func omUserKey(u om.MongoDBUser) string { return userKey(u.Username, u.Database) }
