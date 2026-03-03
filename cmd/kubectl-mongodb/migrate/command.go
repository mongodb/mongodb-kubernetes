package migrate

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/xerrors"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/authentication"
)

const defaultNamespace = "default"

var (
	configMapName      string
	configMapNamespace string
	secretName         string
	secretNamespace    string
)

var MigrateCmd = &cobra.Command{
	Use:   "migrate",
	Short: "Migrate MongoDB deployments to Kubernetes",
	Long: `Validates an Ops Manager automation config and generates MongoDB/MongoDBUser
Kubernetes CRs for migrating existing deployments to the operator.

Requires a ConfigMap (baseUrl, orgId, projectName) and a Secret (publicKey,
privateKey) in the same format used by the operator.`,
}

func init() {
	MigrateCmd.PersistentFlags().StringVar(&configMapName, "config-map-name", "", "Name of the ConfigMap containing the OM URL and project ID (required)")
	MigrateCmd.PersistentFlags().StringVar(&configMapNamespace, "config-map-namespace", defaultNamespace, "Namespace of the ConfigMap. Uses default if not provided")
	MigrateCmd.PersistentFlags().StringVar(&secretName, "secret-name", "", "Name of the Secret containing the OM API credentials (required)")
	MigrateCmd.PersistentFlags().StringVar(&secretNamespace, "secret-namespace", defaultNamespace, "Namespace of the Secret. Uses default if not provided")
	_ = MigrateCmd.MarkPersistentFlagRequired("config-map-name")
	_ = MigrateCmd.MarkPersistentFlagRequired("secret-name")

	MigrateCmd.AddCommand(validateCmd)
	MigrateCmd.AddCommand(generateCmd)
}

var validateCmd = &cobra.Command{
	Use:   "validate",
	Short: "Validate automation config for migration",
	RunE:  runValidate,
}

var generateCmd = &cobra.Command{
	Use:   "generate",
	Short: "Generate MongoDB and MongoDBUser CRs from automation config",
	RunE:  runGenerate,
}

func runValidate(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	conn, _, err := prepareConnection(ctx)
	if err != nil {
		return err
	}
	ac, err := conn.ReadAutomationConfig()
	if err != nil {
		return xerrors.Errorf("error reading automation config: %w", err)
	}

	blockers := ValidateMigrationBlockers(ac)
	errorCount := 0
	for _, b := range blockers {
		fmt.Fprintf(os.Stderr, "[%s] %s\n", b.Severity, b.Message)
		if b.Severity == SeverityError {
			errorCount++
		}
	}

	if errorCount > 0 {
		return xerrors.Errorf("validation failed: %d error(s) found", errorCount)
	}
	fmt.Fprintln(os.Stderr, "Validation complete. No blockers found.")
	return nil
}

func runGenerate(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	conn, kubeClient, err := prepareConnection(ctx)
	if err != nil {
		return err
	}
	ac, err := conn.ReadAutomationConfig()
	if err != nil {
		return xerrors.Errorf("error reading automation config: %w", err)
	}

	opts := GenerateOptions{
		CredentialsSecretName: secretName,
		ConfigMapName:         configMapName,
	}
	yamlOut, resourceName, err := GenerateMongoDBCR(ac, opts)
	if err != nil {
		return xerrors.Errorf("error generating CR: %w", err)
	}
	fmt.Print(yamlOut)

	userCRs, err := GenerateUserCRs(ac, resourceName)
	if err != nil {
		return xerrors.Errorf("error generating user CRs: %w", err)
	}

	if len(userCRs) > 0 {
		scanner := bufio.NewScanner(os.Stdin)
		ns := secretNamespace
		if ns == "" {
			ns = defaultNamespace
		}

		for _, u := range userCRs {
			if u.NeedsPassword && kubeClient != nil {
				fmt.Fprintf(os.Stderr, "Enter password for SCRAM user %q (db: %s): ", u.Username, u.Database)
				if !scanner.Scan() {
					return xerrors.Errorf("error reading password for user %q", u.Username)
				}
				password := strings.TrimSpace(scanner.Text())
				if password == "" {
					return xerrors.Errorf("password for user %q cannot be empty", u.Username)
				}

				user := &om.MongoDBUser{Username: u.Username, Database: u.Database}
				if err := authentication.ConfigureScramCredentials(user, password, ac); err != nil {
					return xerrors.Errorf("error validating password for user %q: %w", u.Username, err)
				}

				sec := corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      u.PasswordSecret,
						Namespace: ns,
					},
					StringData: map[string]string{
						"password": password,
					},
				}
				if err := kubeClient.CreateSecret(ctx, sec); err != nil {
					return xerrors.Errorf("error creating password secret %q for user %q: %w", u.PasswordSecret, u.Username, err)
				}
				fmt.Fprintf(os.Stderr, "Created secret %q in namespace %q\n", u.PasswordSecret, ns)
			}

			fmt.Printf("---\n%s", u.YAML)
		}
	}

	return nil
}
