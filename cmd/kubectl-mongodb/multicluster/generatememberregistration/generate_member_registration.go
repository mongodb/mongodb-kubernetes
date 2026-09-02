package generatememberregistration

import (
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/xerrors"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/mongodb/mongodb-kubernetes/pkg/kubectl-mongodb/memberregistration"
)

var flags struct {
	memberCluster            string
	memberClusterContext     string
	memberClusterNamespace   string
	operatorNamespace        string
	memberClusterLogicalName string
	memberClusterApiServer   string
}

func init() {
	GenerateMemberRegistrationCmd.Flags().StringVar(&flags.memberCluster, "member-cluster", "", "RFC 1123 name of the member cluster; used as the MemberCluster CR's metadata.name and the credential Secret name suffix. Must match the name passed to generate-member-resources. [required]")
	GenerateMemberRegistrationCmd.Flags().StringVar(&flags.memberClusterContext, "member-cluster-context", "", "Kubeconfig context for the member cluster; the command reads the ServiceAccount token and API server URL from it. [required]")
	GenerateMemberRegistrationCmd.Flags().StringVar(&flags.memberClusterNamespace, "member-cluster-namespace", "", "Namespace on the member cluster holding the operator's credentials. [required]")
	GenerateMemberRegistrationCmd.Flags().StringVar(&flags.operatorNamespace, "operator-namespace", "", "Namespace on the operator's cluster where the MemberCluster CR and credential Secret will be created. Must match the operator's installation namespace. [required]")
	GenerateMemberRegistrationCmd.Flags().StringVar(&flags.memberClusterLogicalName, "member-cluster-logical-name", "", "Name that workloads use to reference this member cluster. Only needed when that name is not RFC 1123 compliant (e.g. it contains underscores). [optional, default: --member-cluster]")
	GenerateMemberRegistrationCmd.Flags().StringVar(&flags.memberClusterApiServer, "member-cluster-api-server", "", "API server address of the member cluster; must be reachable from the operator Pod. [optional, default: the server address from --member-cluster-context]")
}

// GenerateMemberRegistrationCmd reads a member cluster's ServiceAccount token and emits the
// credential Secret + MemberCluster CR the operator needs to reach that cluster.
var GenerateMemberRegistrationCmd = &cobra.Command{
	Use:   "generate-member-registration",
	Short: "Emit a credential Secret and MemberCluster CR for a single member cluster",
	Long: `'generate-member-registration' connects to one member cluster, reads the ServiceAccount
token that 'generate-member-resources' created on it, and writes a credential Secret (a
single-context kubeconfig) and a MemberCluster CR as multi-document YAML to stdout.

If the token Secret is not populated yet, the command waits up to a minute for Kubernetes to provision it.
By default the credential kubeconfig uses the API server address from --member-cluster-context; pass --member-cluster-api-server to override it.

Apply the output to the operator's cluster with kubectl, or commit it to Git for GitOps.

Example:

kubectl-mongodb multicluster generate-member-registration --member-cluster=cluster-east --member-cluster-context=east-ctx --member-cluster-namespace=mongodb --operator-namespace=mongodb | kubectl apply --context=central-ctx -f -
`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		opts, err := parseFlags()
		if err != nil {
			return err
		}

		restConfig, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			clientcmd.NewDefaultClientConfigLoadingRules(),
			&clientcmd.ConfigOverrides{CurrentContext: flags.memberClusterContext},
		).ClientConfig()
		if err != nil {
			return xerrors.Errorf("loading kubeconfig context %q: %w", flags.memberClusterContext, err)
		}
		client, err := kubernetes.NewForConfig(restConfig)
		if err != nil {
			return xerrors.Errorf("building client for context %q: %w", flags.memberClusterContext, err)
		}

		out, err := memberregistration.Generate(cmd.Context(), client, restConfig.Host, opts)
		if err != nil {
			return err
		}
		_, err = fmt.Fprint(os.Stdout, out)
		return err
	},
}

func parseFlags() (memberregistration.Options, error) {
	if strings.TrimSpace(flags.memberCluster) == "" ||
		strings.TrimSpace(flags.memberClusterContext) == "" ||
		strings.TrimSpace(flags.memberClusterNamespace) == "" ||
		strings.TrimSpace(flags.operatorNamespace) == "" {
		return memberregistration.Options{}, xerrors.Errorf("non-empty values are required for [member-cluster, member-cluster-context, member-cluster-namespace, operator-namespace]")
	}

	memberClusterLogicalName := flags.memberClusterLogicalName
	if strings.TrimSpace(memberClusterLogicalName) == "" {
		memberClusterLogicalName = flags.memberCluster
	}

	memberClusterApiServer := strings.TrimSpace(flags.memberClusterApiServer)
	if memberClusterApiServer != "" {
		u, err := url.Parse(memberClusterApiServer)
		if err != nil {
			return memberregistration.Options{}, xerrors.Errorf("invalid --member-cluster-api-server %q: %v", memberClusterApiServer, err)
		}
		if u.Scheme == "" || u.Hostname() == "" {
			return memberregistration.Options{}, xerrors.Errorf("invalid --member-cluster-api-server %q: must be an absolute URL with scheme and host", memberClusterApiServer)
		}
	}

	return memberregistration.Options{
		MemberClusterName:        flags.memberCluster,
		MemberClusterNamespace:   flags.memberClusterNamespace,
		OperatorNamespace:        flags.operatorNamespace,
		MemberClusterLogicalName: memberClusterLogicalName,
		MemberClusterApiServer:   memberClusterApiServer,
		TokenWaitTimeout:         memberregistration.DefaultTokenWaitTimeout,
	}, nil
}
