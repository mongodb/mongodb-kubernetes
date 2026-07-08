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

	userv1 "github.com/mongodb/mongodb-kubernetes/api/v1/user"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/authentication"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

type usersFlags struct {
	configMapName        string
	secretName           string
	namespace            string
	outputFile           string
	usersSecretsFile     string
	resourceNameOverride string
}

var uFlags usersFlags

var UsersCmd = &cobra.Command{
	Use:   "users",
	Short: "Generate MongoDBUser Kubernetes CRs",
	Long: `Generate MongoDBUser CRs from an Ops Manager or Cloud Manager automation config.

The automation config is validated before output is produced. The command exits
with an error if any blockers are found.

PREREQUISITES

  A ConfigMap and a Secret must exist in the target namespace before running this
  command:

    kubectl create configmap my-project \
      --from-literal=baseUrl=<url> \
      --from-literal=orgId=<id> \
      --from-literal=projectName=<name>

    kubectl create secret generic my-credentials \
      --from-literal=publicKey=<key> \
      --from-literal=privateKey=<key>

  Each SCRAM user also requires a pre-created Secret containing their password
  under the key "password":

    kubectl create secret generic <secret-name> \
      --from-literal=password=<password> \
      -n <namespace>

USAGE

  Interactive mode: the command prompts for each user's Secret name in turn.
  Press Enter to accept the suggested name (<username>-password).

  Non-interactive mode: supply --users-secrets-file with a CSV file mapping
  each user to a pre-created Secret:

    # username:database,secret-name
    alice:admin,alice-password
    bob:reporting,bob-password

EXAMPLES

  Interactive:
    kubectl mongodb migrate users \
      --config-map-name my-project \
      --secret-name my-credentials \
      --namespace mongodb

  Non-interactive:
    kubectl mongodb migrate users \
      --config-map-name my-project \
      --secret-name my-credentials \
      --namespace mongodb \
      --users-secrets-file users-secrets.csv`,
	RunE: runGenerateUsers,
}

func init() {
	UsersCmd.Flags().StringVar(&uFlags.configMapName, "config-map-name", "", "Name of the ConfigMap containing the OM connection details (baseUrl, orgId, projectName) (required)")
	UsersCmd.Flags().StringVar(&uFlags.secretName, "secret-name", "", "Name of the Secret containing the OM API credentials (publicKey, privateKey) (required)")
	UsersCmd.Flags().StringVar(&uFlags.namespace, "namespace", defaultNamespace, "Namespace of the ConfigMap and Secret")
	UsersCmd.Flags().StringVarP(&uFlags.outputFile, "output", "o", "", "Write generated CRs to this file instead of stdout")
	UsersCmd.Flags().StringVar(&uFlags.usersSecretsFile, "users-secrets-file", "", "CSV file mapping 'username:database,secret-name' for SCRAM users. Each line maps one user to a pre-created Kubernetes Secret. When omitted, the command prompts for each secret name interactively")
	UsersCmd.Flags().StringVar(&uFlags.resourceNameOverride, "resource-name-override", "", "Name of the MongoDB replica set CR that the generated MongoDBUser resources will reference via mongodbResourceRef.name. Defaults to the normalized replica set name from the automation config")
	_ = UsersCmd.MarkFlagRequired("config-map-name")
	_ = UsersCmd.MarkFlagRequired("secret-name")
}

func runGenerateUsers(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()

	conn, kubeClient, err := prepareConnection(ctx, uFlags.namespace, uFlags.configMapName, uFlags.secretName)
	if err != nil {
		return err
	}

	ac, _, _, err := fetchAndValidate(conn)
	if err != nil {
		return err
	}

	mongodbResourceName := uFlags.resourceNameOverride
	if mongodbResourceName == "" {
		var err error
		mongodbResourceName, err = resolveMongoDBResourceName(ac, "")
		if err != nil {
			return err
		}
	}

	opts, err := buildUsersOptions(ctx, kubeClient, ac, os.Stdin, uFlags.namespace, uFlags.usersSecretsFile)
	if err != nil {
		return err
	}

	userObjects, err := GenerateUserCRs(ac, mongodbResourceName, uFlags.namespace, opts)
	if err != nil {
		return err
	}

	resources, err := renderObjects(userObjects)
	if err != nil {
		return err
	}
	return writeOutput(resources, uFlags.outputFile)
}

func resolveMongoDBResourceName(ac *om.AutomationConfig, override string) (string, error) {
	if override != "" {
		return override, nil
	}
	if shardedClusters := ac.Deployment.GetShardedClusters(); len(shardedClusters) > 0 {
		name := util.NormalizeName(shardedClusters[0].Name())
		if name == "" {
			return "", fmt.Errorf("sharded cluster name %q cannot be normalized to a valid Kubernetes name. Use --resource-name-override to provide one", shardedClusters[0].Name())
		}
		return name, nil
	}
	replicaSets := ac.Deployment.GetReplicaSets()
	if len(replicaSets) == 0 {
		return "", fmt.Errorf("no replica sets found in the automation config")
	}
	name := util.NormalizeName(replicaSets[0].Name())
	if name == "" {
		return "", fmt.Errorf("replica set name %q cannot be normalized to a valid Kubernetes name. Use --resource-name-override to provide one", replicaSets[0].Name())
	}
	return name, nil
}

func buildUsersOptions(ctx context.Context, kubeClient kubernetesClient.Client, ac *om.AutomationConfig, stdin io.Reader, namespace, usersSecretsFile string) (GenerateOptions, error) {
	opts := GenerateOptions{Namespace: namespace}
	scanner := bufio.NewScanner(stdin)
	if err := collectUserCreds(ctx, kubeClient, ac, &opts, scanner, usersSecretsFile); err != nil {
		return GenerateOptions{}, err
	}
	return opts, nil
}

func userKey(username, database string) string { return username + ":" + database }

func suggestedUserSecretName(user *om.MongoDBUser) string {
	return userv1.NormalizeName(user.Username) + "-password"
}

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
	return collectUserSecretNamesInteractively(ctx, kubeClient, ac, opts, scanner)
}

func collectUserSecretNamesInteractively(ctx context.Context, kubeClient kubernetesClient.Client, ac *om.AutomationConfig, opts *GenerateOptions, scanner *bufio.Scanner) error {
	users := scramUsers(ac)
	if len(users) == 0 {
		return nil
	}

	_, _ = fmt.Fprintf(promptOutput, "SCRAM users found. Create a Kubernetes Secret for each user before continuing:\n\n")
	_, _ = fmt.Fprintf(promptOutput, "  kubectl create secret generic <secret-name> --from-literal=password=<password> -n %s\n\n", opts.Namespace)

	opts.ExistingUserSecrets = make(map[string]string)
	for _, user := range users {
		suggestedName := suggestedUserSecretName(user)
		secretName, err := promptKubernetesName(scanner, fmt.Sprintf("Secret name for user %q (db: %s) [%s]: ", user.Username, user.Database, suggestedName), suggestedName)
		if err != nil {
			return fmt.Errorf("failed to read spec.passwordSecretKeyRef.name for user %q: %w", user.Username, err)
		}
		if err := validateUserSecret(ctx, kubeClient, user, secretName, ac, opts.Namespace); err != nil {
			return err
		}
		opts.ExistingUserSecrets[userKey(user.Username, user.Database)] = secretName
	}
	return nil
}

func validatePasswordAgainstOM(username, database, password string, ac *om.AutomationConfig) error {
	_, acUser := ac.Auth.GetUser(username, database)
	matches, err := authentication.PasswordMatchesStoredCreds(username, password, acUser)
	if err != nil {
		return fmt.Errorf("failed to validate password for user %q: %w", username, err)
	}
	if !matches {
		return fmt.Errorf("password for user %q does not match the existing credentials in Ops Manager", username)
	}
	return nil
}

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

func validateUserSecret(ctx context.Context, kubeClient kubernetesClient.Client, user *om.MongoDBUser, secretName string, ac *om.AutomationConfig, namespace string) error {
	passwordBytes, err := requirePasswordSecret(ctx, kubeClient, namespace, secretName)
	if err != nil {
		return fmt.Errorf("%w for user %q", err, user.Username)
	}
	return validatePasswordAgainstOM(user.Username, user.Database, string(passwordBytes), ac)
}

func parseUsersSecretsFile(path string) (map[string]string, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open %q: %w", path, err)
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
			return nil, fmt.Errorf("line %d: first field %q is missing the database part. Expected \"username:database\"", lineNum, userDB)
		}
		if errs := k8svalidation.IsDNS1123Subdomain(sName); len(errs) > 0 {
			return nil, fmt.Errorf("line %d: secret name %q is not a valid Kubernetes name: %s", lineNum, sName, errs[0])
		}
		result[userDB] = sName
	}
	return result, sc.Err()
}
