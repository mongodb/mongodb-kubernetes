// Package memberresources renders the RBAC a member cluster needs for MCK multi-cluster
// operation, from the operator Helm chart embedded in the plugin binary. It holds the
// rendering logic; the CLI wiring lives in cmd/kubectl-mongodb.
package memberresources

import (
	"io/fs"
	"path"
	"strings"

	"golang.org/x/xerrors"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/engine"

	helmchart "github.com/mongodb/mongodb-kubernetes/helm_chart"
)

// memberTemplates are the chart templates rendered into the output, in order. Everything a
// member cluster needs must be present:
//
//   - member-cluster-rbac.yaml: the member-specific resources — the member ServiceAccount
//     and token Secret (credentials), and mck-member-role-multicluster
//     holding the rules the operator needs only because of multi-cluster operation
//     (deletecollection cleanup; the serviceaccounts get self-read for the parked
//     rbac-version validation was removed with that scaffolding).
//   - operator-roles-base.yaml: dual-mode; in member mode it renders
//     mck-member-role-base — the operator's shared workload-management
//     rules, identical to the base installation's role, from a single unconditional
//     source in the template.
//   - operator-roles-pvc-resize.yaml: dual-mode; in member mode it renders
//     mck-member-pvc-resize bound to the member SA.
//   - database-roles.yaml: RBAC for the MongoDB pods. Dual-mode; in member mode it renders
//     member-scoped names (mck-member-*).
//   - operator-roles-telemetry.yaml: telemetry ClusterRole/ClusterRoleBinding. Dual-mode; in
//     member mode it renders mck-member-cluster-telemetry bound to the member
//     SA. Renders to nothing when operatorTelemetry is false (installClusterRole gate).
//
// All member resources use the distinct fixed mck-member-* naming so they are
// additive to the base-installation RBAC and never collide with it — including when the
// operator's own cluster is also configured as a member cluster. One render can be applied
// to every member cluster.
var memberTemplates = []string{
	"member-cluster-rbac.yaml",
	"operator-roles-base.yaml",
	"operator-roles-pvc-resize.yaml",
	"database-roles.yaml",
	"operator-roles-telemetry.yaml",
}

// Render renders the member-cluster templates from the embedded chart with the given
// member-cluster values and returns the concatenated YAML.
//
//   - workloadNamespaces: namespaces where workloads run on the member cluster; workload
//     ServiceAccounts/Roles are seeded in each, and (unless operatorClusterScoped) each operator
//     role is rendered as a namespaced Role once per binding namespace — the union of the
//     workload namespaces and the member namespace — with a RoleBinding per namespace pointing
//     at the co-located Role (a RoleBinding cannot reference a Role in another namespace).
//     The caller (the CLI) is expected to have normalised and validated the list — the
//     templates reject "*" as a backstop.
//   - operatorClusterScoped: grant the member SA cluster-wide permissions (ClusterRole + a single
//     ClusterRoleBinding); use when the operator watches all namespaces. This is the only mode
//     that renders cluster-scoped operator-permission resources.
//   - operatorTelemetry: also render the telemetry ClusterRole/ClusterRoleBinding (cluster-scoped
//     regardless of the mode above).
//   - imagePullSecrets: when non-empty, set as the workload ServiceAccounts' imagePullSecrets.
func Render(namespace string, workloadNamespaces []string, operatorClusterScoped, operatorTelemetry bool, imagePullSecrets string) (string, error) {
	chrt, err := loadEmbeddedChart()
	if err != nil {
		return "", xerrors.Errorf("loading embedded chart: %w", err)
	}

	values := map[string]any{
		"memberCluster": map[string]any{
			"enabled":            true,
			"clusterScoped":      operatorClusterScoped,
			"workloadNamespaces": workloadNamespaces,
		},
		"operator": map[string]any{
			"namespace": namespace,
			"telemetry": map[string]any{
				"installClusterRole": operatorTelemetry,
			},
		},
	}
	if imagePullSecrets != "" {
		values["registry"] = map[string]any{
			"imagePullSecrets": imagePullSecrets,
		}
	}

	renderValues, err := chartutil.ToRenderValues(chrt, values, chartutil.ReleaseOptions{
		Namespace: namespace,
	}, chartutil.DefaultCapabilities)
	if err != nil {
		return "", xerrors.Errorf("building render values: %w", err)
	}

	rendered, err := engine.Render(chrt, renderValues)
	if err != nil {
		return "", xerrors.Errorf("rendering chart: %w", err)
	}

	var out strings.Builder
	for _, tmpl := range memberTemplates {
		content := strings.TrimSpace(rendered[path.Join(chrt.Name(), "templates", tmpl)])
		if content == "" {
			continue
		}
		out.WriteString(content)
		out.WriteString("\n")
	}
	return out.String(), nil
}

func loadEmbeddedChart() (*chart.Chart, error) {
	var files []*loader.BufferedFile
	err := fs.WalkDir(helmchart.ChartFiles, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := helmchart.ChartFiles.ReadFile(p)
		if err != nil {
			return err
		}
		// BufferedFile names must be chart-root-relative, which is exactly how the
		// files are rooted in the embedded FS.
		files = append(files, &loader.BufferedFile{Name: p, Data: data})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return loader.LoadFiles(files)
}
