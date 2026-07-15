package generatememberresources

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/xerrors"

	"github.com/mongodb/mongodb-kubernetes/pkg/kubectl-mongodb/memberresources"
)

var flags struct {
	memberCluster          string
	memberClusterNamespace string
	watchedNamespaces      string
}

func init() {
	GenerateMemberResourcesCmd.Flags().StringVar(&flags.memberCluster, "member-cluster", "", "Name of the member cluster; used in RBAC resource names (mck-member-<cluster-name>-*) and as the cluster identity. [required]")
	GenerateMemberResourcesCmd.Flags().StringVar(&flags.memberClusterNamespace, "member-cluster-namespace", "", "Namespace on the member cluster where the operator will manage workloads. [required]")
	GenerateMemberResourcesCmd.Flags().StringVar(&flags.watchedNamespaces, "watched-namespaces", "", "Comma-separated namespaces the operator should watch on this member cluster. [optional, default: --member-cluster-namespace]")
}

// GenerateMemberResourcesCmd renders member-cluster RBAC from the embedded Helm chart.
var GenerateMemberResourcesCmd = &cobra.Command{
	Use:   "generate-member-resources",
	Short: "Render member-cluster RBAC manifests for a single member cluster",
	Long: `'generate-member-resources' outputs the RBAC a member cluster needs for
multi-cluster operation. It is purely local: it contacts no cluster and writes the
manifests as YAML to stdout.

Apply the output to the member cluster with kubectl, or commit it to Git for GitOps.

Example:

kubectl-mongodb multicluster generate-member-resources --member-cluster=cluster-east --member-cluster-namespace=mongodb | kubectl apply --context=east-ctx -f -
`,
	RunE: func(_ *cobra.Command, _ []string) error {
		watched, err := parseFlags()
		if err != nil {
			return err
		}

		out, err := memberresources.Render(flags.memberCluster, flags.memberClusterNamespace, watched)
		if err != nil {
			return err
		}
		_, err = fmt.Fprint(os.Stdout, out)
		return err
	},
}

func parseFlags() ([]string, error) {
	if strings.TrimSpace(flags.memberCluster) == "" || strings.TrimSpace(flags.memberClusterNamespace) == "" {
		return nil, xerrors.Errorf("non-empty values are required for [member-cluster, member-cluster-namespace]")
	}
	if strings.TrimSpace(flags.watchedNamespaces) == "" {
		return []string{flags.memberClusterNamespace}, nil
	}
	return strings.Split(flags.watchedNamespaces, ","), nil
}
