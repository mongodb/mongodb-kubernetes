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
	workloadNamespaces     string
	clusterScoped          bool
	createTelemetryRoles   bool
	imagePullSecrets       string
}

func init() {
	GenerateMemberResourcesCmd.Flags().StringVar(&flags.memberCluster, "member-cluster", "", "Name of the member cluster; used in RBAC resource names (mck-member-<cluster-name>-*) and as the cluster identity. [required]")
	GenerateMemberResourcesCmd.Flags().StringVar(&flags.memberClusterNamespace, "member-cluster-namespace", "", "Namespace on the member cluster where the member ServiceAccount and its token Secret are created (the credential namespace). [required]")
	GenerateMemberResourcesCmd.Flags().StringVar(&flags.workloadNamespaces, "workload-namespaces", "", "Comma-separated namespaces on the member cluster where MongoDB/Ops Manager workloads will run. [optional, default: --member-cluster-namespace]")
	GenerateMemberResourcesCmd.Flags().BoolVar(&flags.clusterScoped, "cluster-scoped", false, "Grant the member ServiceAccount cluster-wide permissions (ClusterRole with a single ClusterRoleBinding instead of per-namespace RoleBindings). Use when the operator watches all namespaces. [optional]")
	GenerateMemberResourcesCmd.Flags().BoolVar(&flags.createTelemetryRoles, "create-telemetry-roles", true, "Create the telemetry ClusterRole and ClusterRoleBinding for the member ServiceAccount. Set to false to opt out of telemetry. [optional]")
	GenerateMemberResourcesCmd.Flags().StringVar(&flags.imagePullSecrets, "image-pull-secrets", "", "Name of an existing image pull Secret to set on the member-cluster workload ServiceAccounts, for pulling images from a private registry. The Secret must already exist in the workload namespace on the member cluster. [optional]")
}

// GenerateMemberResourcesCmd renders member-cluster RBAC from the embedded Helm chart.
var GenerateMemberResourcesCmd = &cobra.Command{
	Use:   "generate-member-resources",
	Short: "Render member-cluster RBAC manifests for a single member cluster",
	Long: `'generate-member-resources' outputs the RBAC a member cluster needs for
multi-cluster operation. It is purely local: it contacts no cluster and writes the
manifests as YAML to stdout.

Apply the output to the member cluster with kubectl, or commit it to Git for GitOps.

Example (operator watching a single namespace):

kubectl-mongodb multicluster generate-member-resources --member-cluster=cluster-east --member-cluster-namespace=mongodb | kubectl apply --context=east-ctx -f -

Example (cluster-wide operator with workloads in two namespaces):

kubectl-mongodb multicluster generate-member-resources --member-cluster=cluster-east --member-cluster-namespace=mongodb --cluster-scoped --workload-namespaces=om,mdb | kubectl apply --context=east-ctx -f -
`,
	RunE: func(_ *cobra.Command, _ []string) error {
		workloadNamespaces, err := parseFlags()
		if err != nil {
			return err
		}

		out, err := memberresources.Render(flags.memberCluster, flags.memberClusterNamespace, workloadNamespaces, flags.clusterScoped, flags.createTelemetryRoles, flags.imagePullSecrets)
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
	return normalizeWorkloadNamespaces(flags.workloadNamespaces, flags.memberClusterNamespace)
}

// normalizeWorkloadNamespaces turns the raw --workload-namespaces flag value into the list
// of namespaces to render for: it trims entries, rejects empty entries and "*", and dedups.
// A blank value defaults to the member-cluster namespace. "*" is never valid: workload RBAC
// is namespaced, so a cluster-wide operator must use --cluster-scoped instead (which covers
// the member SA's permissions cluster-wide while workload SAs stay namespaced).
func normalizeWorkloadNamespaces(rawValue, memberClusterNamespace string) ([]string, error) {
	if strings.TrimSpace(rawValue) == "" {
		return []string{memberClusterNamespace}, nil
	}
	seen := make(map[string]bool)
	var namespaces []string
	for _, entry := range strings.Split(rawValue, ",") {
		ns := strings.TrimSpace(entry)
		if ns == "" {
			return nil, xerrors.Errorf("invalid --workload-namespaces %q: entries must be non-empty namespace names", rawValue)
		}
		if ns == "*" {
			return nil, xerrors.Errorf("'*' is not a valid workload namespace; use --cluster-scoped for a cluster-wide operator")
		}
		if !seen[ns] {
			seen[ns] = true
			namespaces = append(namespaces, ns)
		}
	}
	return namespaces, nil
}
