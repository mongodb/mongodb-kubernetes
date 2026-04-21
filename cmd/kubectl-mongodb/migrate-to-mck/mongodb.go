package migratetomck

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"sigs.k8s.io/controller-runtime/pkg/client"

	k8svalidation "k8s.io/apimachinery/pkg/util/validation"

	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
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
	Long: `Generates a MongoDB Kubernetes CR from an Ops Manager/Cloud Manager
automation config. The automation config is validated before generation. If any blockers are
found, the command fails without producing output.

Requires a ConfigMap (baseUrl, orgId, projectName) and a Secret (publicKey,
privateKey) in the same format used by the operator.

Example:

kubectl mongodb migrate mongodb --config-map-name my-project --secret-name my-credentials --namespace mongodb`,
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

	mongodbCR, _, err := GenerateMongoDBCR(ac, opts)
	if err != nil {
		return err
	}

	extra := generateExtraResources(ac, opts)
	objects := make([]client.Object, 0, 1+len(extra))
	objects = append(objects, mongodbCR)
	objects = append(objects, extra...)

	resources, err := marshalMultiDoc(objects)
	if err != nil {
		return err
	}
	return writeOutput(resources, mFlags.outputFile)
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
	if prefix == "" {
		for {
			p, err := promptLine(scanner, "Enter value for security.certsSecretPrefix (e.g. mdb): ")
			if err != nil {
				return fmt.Errorf("failed to read certsSecretPrefix: %w", err)
			}
			if p == "" {
				_, _ = fmt.Fprintln(promptOutput, "certsSecretPrefix cannot be empty, please try again.")
				continue
			}
			if errs := k8svalidation.IsDNS1123Subdomain(p); len(errs) > 0 {
				_, _ = fmt.Fprintf(promptOutput, "%q is not a valid Kubernetes name: %s. Please try again.\n", p, errs[0])
				continue
			}
			prefix = p
			break
		}
	} else if errs := k8svalidation.IsDNS1123Subdomain(prefix); len(errs) > 0 {
		return fmt.Errorf("--certs-secret-prefix value %q is not a valid Kubernetes resource name: %s", prefix, errs[0])
	}
	opts.CertsSecretPrefix = prefix
	return nil
}

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
