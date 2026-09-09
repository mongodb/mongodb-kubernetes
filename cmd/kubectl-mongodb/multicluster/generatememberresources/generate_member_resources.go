package generatememberresources

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/xerrors"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/mongodb/mongodb-kubernetes/pkg/kubectl-mongodb/memberresources"
)

var flags struct {
	memberClusterNamespace      string
	memberClusterServiceAccount string
	workloadNamespaces          string
	operatorClusterScoped       bool
	operatorTelemetry           bool
	imagePullSecrets            string
}

func init() {
	GenerateMemberResourcesCmd.Flags().StringVar(&flags.memberClusterNamespace, "member-cluster-namespace", "", "Namespace on the member cluster for the operator's credentials. [required]")
	GenerateMemberResourcesCmd.Flags().StringVar(&flags.memberClusterServiceAccount, "member-cluster-service-account", "", "Name of the ServiceAccount you created on the member cluster for the operator to authenticate as; the rendered RBAC is bound to it. [required]")
	GenerateMemberResourcesCmd.Flags().StringVar(&flags.workloadNamespaces, "workload-namespaces", "", "Comma-separated namespaces on the member cluster where MongoDB/Ops Manager workloads will run. [optional, default: --member-cluster-namespace]")
	// --operator-cluster-scoped is a distinct flag rather than "*" in --workload-namespaces for
	// two reasons: workload namespaces are placement (you cannot create a ServiceAccount in "*"),
	// and a cluster-wide operator builds its member-cluster caches without DefaultNamespaces
	// (main.go), so informers issue cluster-scoped LIST/WATCH calls — which RBAC only authorises
	// via ClusterRole+ClusterRoleBinding (namespaced Roles never match a namespace-less request
	// path). Namespaced-only member RBAC therefore cannot serve an operator watching all
	// namespaces.
	GenerateMemberResourcesCmd.Flags().BoolVar(&flags.operatorClusterScoped, "operator-cluster-scoped", false, "Grant the operator access to all namespaces on this member cluster. Use when the operator is installed cluster-wide (watches all namespaces). Without this flag, the rendered operator permissions are namespaced Roles bound in each workload namespace. [optional]")
	GenerateMemberResourcesCmd.Flags().BoolVar(&flags.operatorTelemetry, "operator-telemetry", true, "Allow the operator to collect cluster-level telemetry on this member cluster. Set to false to opt out. [optional]")
	GenerateMemberResourcesCmd.Flags().StringVar(&flags.imagePullSecrets, "image-pull-secrets", "", "Name of an existing image pull Secret to set on the member-cluster workload ServiceAccounts, for pulling images from a private registry. The Secret must already exist in the workload namespace on the member cluster. [optional]")
}

// GenerateMemberResourcesCmd renders member-cluster RBAC from the embedded Helm chart.
var GenerateMemberResourcesCmd = &cobra.Command{
	Use:   "generate-member-resources",
	Short: "Render member-cluster RBAC manifests for a single member cluster",
	Long: `'generate-member-resources' outputs the RBAC a member cluster needs for
multi-cluster operation. It is purely local: it contacts no cluster and writes the
manifests as YAML to stdout.

The output does not include the operator's credentials: create the ServiceAccount
and its token Secret on the member cluster yourself (day-2 configuration), then pass
the ServiceAccount name via --member-cluster-service-account so the rendered RBAC
binds to it.

Apply the output to the member cluster with kubectl, or commit it to Git for GitOps.

By default the operator's permissions are namespaced: each operator role is rendered
as a Role in every workload namespace (plus the member-cluster namespace) with a
RoleBinding alongside it. --operator-cluster-scoped switches to a ClusterRole with a
single ClusterRoleBinding for a cluster-wide operator.

Example (operator watching a single namespace):

kubectl-mongodb multicluster generate-member-resources --member-cluster-namespace=mongodb --member-cluster-service-account=mck-member-sa | kubectl apply --context=east-ctx -f -

Example (cluster-wide operator with workloads in two namespaces):

kubectl-mongodb multicluster generate-member-resources --member-cluster-namespace=mongodb --member-cluster-service-account=mck-member-sa --operator-cluster-scoped --workload-namespaces=om,mdb | kubectl apply --context=east-ctx -f -
`,
	RunE: func(_ *cobra.Command, _ []string) error {
		workloadNamespaces, err := parseFlags()
		if err != nil {
			return err
		}

		out, err := memberresources.Render(flags.memberClusterNamespace, flags.memberClusterServiceAccount, workloadNamespaces, flags.operatorClusterScoped, flags.operatorTelemetry, flags.imagePullSecrets)
		if err != nil {
			return err
		}
		_, err = fmt.Fprint(os.Stdout, out)
		return err
	},
}

func parseFlags() ([]string, error) {
	if strings.TrimSpace(flags.memberClusterNamespace) == "" ||
		strings.TrimSpace(flags.memberClusterServiceAccount) == "" {
		return nil, xerrors.Errorf("non-empty values are required for [member-cluster-namespace, member-cluster-service-account]")
	}
	// The ServiceAccount name lands in every rendered binding's subject, where an invalid
	// name would only surface as an apiserver rejection at apply time — fail fast instead.
	if errs := validation.IsDNS1123Subdomain(flags.memberClusterServiceAccount); len(errs) > 0 {
		return nil, xerrors.Errorf("invalid --member-cluster-service-account %q: %s", flags.memberClusterServiceAccount, strings.Join(errs, "; "))
	}
	return normalizeWorkloadNamespaces(flags.workloadNamespaces, flags.memberClusterNamespace)
}

// normalizeWorkloadNamespaces turns the raw --workload-namespaces flag value into the list
// of namespaces to render for: it trims entries, rejects empty entries and "*", and dedups.
// A blank value defaults to the member-cluster namespace. "*" is never valid: workload RBAC
// is namespaced, so a cluster-wide operator must use --operator-cluster-scoped instead (which covers
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
			return nil, xerrors.Errorf("'*' is not a valid workload namespace; use --operator-cluster-scoped for a cluster-wide operator")
		}
		if !seen[ns] {
			seen[ns] = true
			namespaces = append(namespaces, ns)
		}
	}
	return namespaces, nil
}
