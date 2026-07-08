package migratetomck

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
	"sigs.k8s.io/controller-runtime/pkg/client"

	k8svalidation "k8s.io/apimachinery/pkg/util/validation"

	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/passwordhash"
	pkgtls "github.com/mongodb/mongodb-kubernetes/pkg/tls"
)

type mongodbFlags struct {
	configMapName        string
	secretName           string
	namespace            string
	outputFile           string
	resourceNameOverride string
	certsSecretPrefix    string
	prometheusSecretName string
}

var mFlags mongodbFlags

var MongodbCmd = &cobra.Command{
	Use:   "mongodb",
	Short: "Generate a MongoDB Kubernetes CR",
	Long: `Generate a MongoDB CR from an Ops Manager or Cloud Manager automation config.

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

  If Prometheus is enabled, create a Secret containing the Prometheus password:

    kubectl create secret generic <secret-name> \
      --from-literal=password=<password> \
      -n <namespace>

  If mongod TLS is enabled, create the ConfigMap and Secret referenced by the
  generated CR:

    kubectl create configmap <resourceName>-ca \
      --from-file=ca-pem=<ca-file> \
      -n <namespace>

    kubectl create secret tls <certsSecretPrefix>-<resourceName>-cert \
      --cert=<server-cert> \
      --key=<server-key> \
      -n <namespace>

  If MONGODB-X509 agent authentication is enabled, create the agent certificate
  Secret referenced by spec.security.authentication.agents.clientCertificateSecretRef:

    kubectl create secret tls <certsSecretPrefix>-<resourceName>-agent-certs \
      --cert=<agent-cert> \
      --key=<agent-key> \
      -n <namespace>

USAGE

  When TLS is enabled, the command prompts for spec.security.certsSecretPrefix.
  Pass --certs-secret-prefix to skip the prompt.

  When Prometheus is enabled, the command prompts for the name of the password
  Secret. Pass --prometheus-secret-name to skip the prompt.

EXAMPLES

  Interactive:
    kubectl mongodb migrate mongodb \
      --config-map-name my-project \
      --secret-name my-credentials \
      --namespace mongodb

  Non-interactive:
    kubectl mongodb migrate mongodb \
      --config-map-name my-project \
      --secret-name my-credentials \
      --namespace mongodb \
      --certs-secret-prefix mdb \
      --prometheus-secret-name prom-secret`,
	RunE: runGenerateMongodb,
}

func init() {
	MongodbCmd.Flags().StringVar(&mFlags.configMapName, "config-map-name", "", "Name of the ConfigMap containing the OM connection details (baseUrl, orgId, projectName) (required)")
	MongodbCmd.Flags().StringVar(&mFlags.secretName, "secret-name", "", "Name of the Secret containing the OM API credentials (publicKey, privateKey) (required)")
	MongodbCmd.Flags().StringVar(&mFlags.namespace, "namespace", defaultNamespace, "Namespace of the ConfigMap and Secret")
	MongodbCmd.Flags().StringVarP(&mFlags.outputFile, "output", "o", "", "Write generated CRs to this file instead of stdout")
	MongodbCmd.Flags().StringVar(&mFlags.resourceNameOverride, "resource-name-override", "", "Kubernetes resource name (metadata.name) for the generated CR. When the replica set name is not a valid Kubernetes name it is auto-normalized, but this flag lets you choose a custom name. spec.replicaSetNameOverride is set automatically")
	MongodbCmd.Flags().StringVar(&mFlags.certsSecretPrefix, "certs-secret-prefix", "", "Value for spec.security.certsSecretPrefix. Required when TLS is enabled and suppresses the interactive prompt")
	MongodbCmd.Flags().StringVar(&mFlags.prometheusSecretName, "prometheus-secret-name", "", "Name of a pre-created Kubernetes Secret containing the Prometheus password (key: \"password\"). Suppresses the interactive prompt")
	_ = MongodbCmd.MarkFlagRequired("config-map-name")
	_ = MongodbCmd.MarkFlagRequired("secret-name")
}

func runGenerateMongodb(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()

	conn, kubeClient, err := prepareConnection(ctx, mFlags.namespace, mFlags.configMapName, mFlags.secretName)
	if err != nil {
		return err
	}

	ac, projectConfigs, sourceProcess, err := fetchAndValidate(conn)
	if err != nil {
		return err
	}

	opts, err := buildMongodbOptions(ctx, kubeClient, ac, projectConfigs, sourceProcess, os.Stdin, mFlags)
	if err != nil {
		return err
	}

	objects, err := generateMongodbObjects(ac, opts)
	if err != nil {
		return err
	}
	resources, err := renderObjects(objects)
	if err != nil {
		return err
	}
	return writeOutput(resources, mFlags.outputFile)
}

func generateMongodbObjects(ac *om.AutomationConfig, opts GenerateOptions) ([]client.Object, error) {
	mongodbCR, _, err := GenerateMongoDBCR(ac, opts)
	if err != nil {
		return nil, err
	}
	extra := generateExtraResources(ac, opts)
	return append([]client.Object{mongodbCR}, extra...), nil
}

func buildMongodbOptions(ctx context.Context, kubeClient kubernetesClient.Client, ac *om.AutomationConfig, projectConfigs *ProjectConfigs, sourceProcess *om.Process, stdin io.Reader, flags mongodbFlags) (GenerateOptions, error) {
	opts := GenerateOptions{
		ResourceNameOverride:  flags.resourceNameOverride,
		CredentialsSecretName: flags.secretName,
		ConfigMapName:         flags.configMapName,
		Namespace:             flags.namespace,
		ProjectConfigs:        projectConfigs,
		SourceProcess:         sourceProcess,
	}

	scanner := bufio.NewScanner(stdin)

	if err := ensureTLS(ac, &opts, scanner, flags.certsSecretPrefix); err != nil {
		return GenerateOptions{}, err
	}
	if err := collectPrometheusCreds(ctx, kubeClient, ac, &opts, scanner, flags.prometheusSecretName); err != nil {
		return GenerateOptions{}, err
	}
	return opts, nil
}

func isTLSEnabled(processMap map[string]om.Process) bool {
	for _, proc := range processMap {
		if len(proc.NetTLSSections()) > 0 && pkgtls.GetTLSModeFromMongodConfig(proc.Args()) != pkgtls.Disabled {
			return true
		}
	}
	return false
}

func ensureTLS(ac *om.AutomationConfig, opts *GenerateOptions, scanner *bufio.Scanner, certsSecretPrefix string) error {
	if !isTLSEnabled(ac.Deployment.ProcessMap()) {
		return nil
	}
	prefix := certsSecretPrefix
	if prefix != "" {
		if errs := k8svalidation.IsDNS1123Subdomain(prefix); len(errs) > 0 {
			return fmt.Errorf("spec.security.certsSecretPrefix value %q is not a valid Kubernetes resource name: %s", prefix, errs[0])
		}
	} else {
		var err error
		prefix, err = promptKubernetesName(scanner, "Enter value for security.certsSecretPrefix (e.g. mdb): ", "")
		if err != nil {
			return fmt.Errorf("failed to read spec.security.certsSecretPrefix: %w", err)
		}
	}
	opts.CertsSecretPrefix = prefix
	return nil
}

func collectPrometheusCreds(ctx context.Context, kubeClient kubernetesClient.Client, ac *om.AutomationConfig, opts *GenerateOptions, scanner *bufio.Scanner, prometheusSecretName string) error {
	acProm := ac.Deployment.GetPrometheus()
	if acProm == nil || !acProm.Enabled || acProm.Username == "" {
		return nil
	}
	secretName := prometheusSecretName
	if secretName == "" {
		var err error
		secretName, err = promptKubernetesName(scanner, fmt.Sprintf("Secret name for Prometheus user %q [%s]: ", acProm.Username, PrometheusPasswordSecretName), PrometheusPasswordSecretName)
		if err != nil {
			return fmt.Errorf("failed to read spec.prometheus.passwordSecretRef.name: %w", err)
		}
	}
	passwordBytes, err := requirePasswordSecret(ctx, kubeClient, opts.Namespace, secretName)
	if err != nil {
		return err
	}
	opts.PrometheusSecretName = secretName
	opts.PrometheusPassword = string(passwordBytes)

	if acProm.PasswordSalt != "" {
		match, err := passwordhash.PasswordMatchesHash(opts.PrometheusPassword, acProm.PasswordHash, acProm.PasswordSalt)
		if err != nil {
			return fmt.Errorf("failed to verify Prometheus password against automation config: %w", err)
		}
		if !match {
			return fmt.Errorf("password in Secret %q for Prometheus user %q does not match the password in the Ops Manager automation config", secretName, acProm.Username)
		}
	}
	return nil
}
