package generatememberregistration

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/xerrors"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/mongodb/mongodb-kubernetes/pkg/kubectl-mongodb/memberregistration"
)

var flags struct {
	memberCluster          string
	memberClusterContext   string
	memberClusterNamespace string
	operatorNamespace      string
	clusterName            string
}

func init() {
	GenerateMemberRegistrationCmd.Flags().StringVar(&flags.memberCluster, "member-cluster", "", "RFC 1123 name of the member cluster; used as the MemberCluster CR's metadata.name and the credential Secret name suffix. Must match the name passed to generate-member-resources. [required]")
	GenerateMemberRegistrationCmd.Flags().StringVar(&flags.memberClusterContext, "member-cluster-context", "", "Kubeconfig context for the member cluster; the command reads the ServiceAccount token and API server URL from it. [required]")
	GenerateMemberRegistrationCmd.Flags().StringVar(&flags.memberClusterNamespace, "member-cluster-namespace", "", "Namespace on the member cluster where the ServiceAccount token Secret lives. [required]")
	GenerateMemberRegistrationCmd.Flags().StringVar(&flags.operatorNamespace, "operator-namespace", "", "Namespace on the operator's cluster where the MemberCluster CR and credential Secret will be created. Must match the operator's installation namespace. [required]")
	GenerateMemberRegistrationCmd.Flags().StringVar(&flags.clusterName, "cluster-name", "", "Logical cluster name set as spec.clusterName on the MemberCluster CR, used to resolve clusterSpecList[].clusterName references in workload CRs. [optional, default: --member-cluster]")
}

// GenerateMemberRegistrationCmd reads a member cluster's ServiceAccount token and emits the
// credential Secret + MemberCluster CR the operator needs to reach that cluster.
var GenerateMemberRegistrationCmd = &cobra.Command{
	Use:   "generate-member-registration",
	Short: "Emit a credential Secret and MemberCluster CR for a single member cluster",
	Long: `'generate-member-registration' connects to one member cluster, reads the ServiceAccount
token that 'generate-member-resources' created on it, and writes a credential Secret (a
single-context kubeconfig) and a MemberCluster CR as multi-document YAML to stdout.

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

	clusterName := flags.clusterName
	if strings.TrimSpace(clusterName) == "" {
		clusterName = flags.memberCluster
	}

	return memberregistration.Options{
		MemberClusterName:        flags.memberCluster,
		MemberClusterNamespace:   flags.memberClusterNamespace,
		OperatorNamespace:        flags.operatorNamespace,
		MemberClusterLogicalName: clusterName,
	}, nil
}
