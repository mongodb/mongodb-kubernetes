package generateresources

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/xerrors"

	"github.com/mongodb/mongodb-kubernetes/pkg/kubectl-mongodb/memberresources"
)

var flags struct {
	memberCluster            string
	operatorNamespace        string
	workloadNamespaces       string
	operatorClusterScoped    bool
	operatorTelemetry        bool
	workloadImagePullSecrets string
}

func init() {
	GenerateResourcesCmd.Flags().StringVar(&flags.memberCluster, "member-cluster", "", "Logical name of the member cluster this render is for; workloads use it to reference the cluster. [required]")
	GenerateResourcesCmd.Flags().StringVar(&flags.operatorNamespace, "operator-namespace", "", "Namespace where the operator is installed. The operator's credentials (ServiceAccount, Roles, RoleBindings) will be created in this namespace on this cluster. [required]")
	GenerateResourcesCmd.Flags().StringVar(&flags.workloadNamespaces, "workload-namespaces", "", "Comma-separated namespaces on the member cluster where MongoDB/Ops Manager workloads will run. [optional, default: --operator-namespace]")
	// --operator-cluster-scoped is a distinct flag rather than "*" in --workload-namespaces for
	// two reasons: workload namespaces are placement (you cannot create a ServiceAccount in "*"),
	// and a cluster-wide operator builds its member-cluster caches without DefaultNamespaces,
	// so informers issue cluster-scoped LIST/WATCH calls — which RBAC only authorises
	// via ClusterRole+ClusterRoleBinding (namespaced Roles never match a namespace-less request
	// path). Namespaced-only member RBAC therefore cannot serve an operator watching all
	// namespaces.
	GenerateResourcesCmd.Flags().BoolVar(&flags.operatorClusterScoped, "operator-cluster-scoped", false, "Grant the operator access to all namespaces on this member cluster. Use when the operator is installed cluster-wide (watches all namespaces). Without this flag, the rendered operator permissions are namespaced Roles bound in each workload namespace. [optional]")
	GenerateResourcesCmd.Flags().BoolVar(&flags.operatorTelemetry, "operator-telemetry", true, "Allow the operator to collect cluster-level telemetry on this member cluster. Set to false to opt out. [optional]")
	GenerateResourcesCmd.Flags().StringVar(&flags.workloadImagePullSecrets, "workload-image-pull-secrets", "", "Name of an existing image pull Secret to set on the member-cluster workload ServiceAccounts, for pulling images from a private registry. The Secret must already exist in the workload namespace on the member cluster. [optional]")
}

// GenerateResourcesCmd renders member-cluster RBAC from the embedded Helm chart.
var GenerateResourcesCmd = &cobra.Command{
	Use:   "generate-resources",
	Short: "Render member-cluster RBAC manifests for a single member cluster",
	Long: `'generate-resources' outputs the RBAC a member cluster needs for
multi-cluster operation. It is purely local: it contacts no cluster and writes the
manifests as YAML to stdout.

Apply the output to the member cluster with kubectl, or commit it to Git for GitOps.

By default the operator's permissions are namespaced: each operator role is rendered
as a Role in every workload namespace (plus the operator namespace) with a
RoleBinding alongside it. --operator-cluster-scoped switches to a ClusterRole with a
single ClusterRoleBinding for a cluster-wide operator.

Example (operator watching a single namespace):

kubectl-mongodb cluster generate-resources --member-cluster=cluster-east --operator-namespace=mongodb | kubectl apply --context=east-ctx -f -

Example (cluster-wide operator with workloads in two namespaces):

kubectl-mongodb cluster generate-resources --member-cluster=cluster-east --operator-namespace=mongodb --operator-cluster-scoped --workload-namespaces=om,mdb | kubectl apply --context=east-ctx -f -
`,
	RunE: func(_ *cobra.Command, _ []string) error {
		memberClusterName, workloadNamespaces, err := parseFlags()
		if err != nil {
			return err
		}

		out, err := memberresources.Render(memberClusterName, flags.operatorNamespace, workloadNamespaces, flags.operatorClusterScoped, flags.operatorTelemetry, flags.workloadImagePullSecrets)
		if err != nil {
			return err
		}
		_, err = fmt.Fprint(os.Stdout, out)
		return err
	},
}

func parseFlags() (string, []string, error) {
	if strings.TrimSpace(flags.memberCluster) == "" ||
		strings.TrimSpace(flags.operatorNamespace) == "" {
		return "", nil, xerrors.Errorf("non-empty values are required for [member-cluster, operator-namespace]")
	}

	workloadNamespaces, err := normalizeWorkloadNamespaces(flags.workloadNamespaces, flags.operatorNamespace)
	if err != nil {
		return "", nil, err
	}
	return flags.memberCluster, workloadNamespaces, nil
}

// normalizeWorkloadNamespaces turns the raw --workload-namespaces flag value into the list
// of namespaces to render for: it trims entries, rejects empty entries and "*", and dedups.
// A blank value defaults to the operator namespace. "*" is never valid: workload RBAC
// is namespaced, so a cluster-wide operator must use --operator-cluster-scoped instead (which covers
// the member SA's permissions cluster-wide while workload SAs stay namespaced).
func normalizeWorkloadNamespaces(rawValue, operatorNamespace string) ([]string, error) {
	if strings.TrimSpace(rawValue) == "" {
		return []string{operatorNamespace}, nil
	}
	seen := make(map[string]bool)
	var namespaces []string
	for entry := range strings.SplitSeq(rawValue, ",") {
		ns := strings.TrimSpace(entry)
		if ns == "" {
			return nil, xerrors.Errorf("invalid --workload-namespaces %q: entries must be non-empty namespace names", rawValue)
		}
		if ns == "*" {
			return nil, xerrors.Errorf("'*' is not a valid workload namespace; use --operator-cluster-scoped for a cluster-wide operator")
		}
		if !seen[ns] {
			seen[ns] = true
			namespaces = append(namespaces, ns)
		}
	}
	return namespaces, nil
}
