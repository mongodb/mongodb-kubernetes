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
	"github.com/mongodb/mongodb-kubernetes/pkg/resourcenames"
)

// memberTemplates are the chart templates rendered into the output, in order. Everything a
// member cluster needs must be present:
//
//   - member-cluster-rbac.yaml: the operator's own member RBAC (mck-member-<cluster-name>-* SA,
//     token, Role/ClusterRole, bindings) — additive and distinctly named so it never collides
//     with base-installation RBAC.
//   - database-roles.yaml: RBAC for the MongoDB pods. In member mode it renders
//     member-scoped names (mck-member-<cluster-name>-*), so it is additive to the base
//     installation RBAC just like member-cluster-rbac.yaml.
var memberTemplates = []string{
	"member-cluster-rbac.yaml",
	"database-roles.yaml",
}

// Render renders the member-cluster templates from the embedded chart with the given
// member-cluster values and returns the concatenated YAML. When imagePullSecrets is
// non-empty, it is set as the workload ServiceAccounts' imagePullSecrets.
func Render(clusterName, namespace string, watchedNamespaces []string, imagePullSecrets string) (string, error) {
	chrt, err := loadEmbeddedChart()
	if err != nil {
		return "", xerrors.Errorf("loading embedded chart: %w", err)
	}

	values := map[string]any{
		"memberCluster": map[string]any{
			"enabled": true,
			"name":    clusterName,
		},
		"operator": map[string]any{
			"namespace":      namespace,
			"watchNamespace": strings.Join(watchedNamespaces, ","),
		},
	}
	if imagePullSecrets != "" {
		values["registry"] = map[string]any{
			"imagePullSecrets": imagePullSecrets,
		}
	}

	renderValues, err := chartutil.ToRenderValues(chrt, values, chartutil.ReleaseOptions{
		Name:      resourcenames.MemberClusterResourceName(clusterName),
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
