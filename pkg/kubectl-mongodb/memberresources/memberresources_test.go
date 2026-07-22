package memberresources

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	utilyaml "k8s.io/apimachinery/pkg/util/yaml"

	helmchart "github.com/mongodb/mongodb-kubernetes/helm_chart"
)

const diskChartDir = "../../../helm_chart"

// parseResources decodes a multi-document YAML manifest into Kubernetes objects. The
// documents are of mixed kinds (ServiceAccount/Secret/Role/ClusterRole/bindings), so we
// decode into unstructured objects and read fields via the typed accessors.
func parseResources(t *testing.T, manifest string) []*unstructured.Unstructured {
	t.Helper()
	var out []*unstructured.Unstructured
	dec := utilyaml.NewYAMLOrJSONDecoder(strings.NewReader(manifest), 4096)
	for {
		obj := &unstructured.Unstructured{}
		err := dec.Decode(obj)
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err, "failed to parse rendered manifest")
		// Skip empty documents (e.g. a leading/trailing "---").
		if obj.GetKind() == "" {
			continue
		}
		out = append(out, obj)
	}
	return out
}

func kindNames(rs []*unstructured.Unstructured) []string {
	var out []string
	for _, r := range rs {
		out = append(out, r.GetKind()+"/"+r.GetName())
	}
	return out
}

// resourceNames is the single source of truth for the names Render emits, keyed by semantic
// purpose rather than by naming convention. Tests identify resources through these fields so
// they keep isolating the operator RBAC from the workload RBAC once the workload names also
// become member-scoped.
type resourceNames struct {
	// Operator member RBAC, cluster-scoped-named as mck-member-<cluster>-*.
	operatorSA      string
	operatorToken   string
	operatorRole    string
	operatorBinding string

	// Database-workload RBAC.
	// TODO(m1kola): slice-1: these workload names are fixed today because the operator
	// hardcodes the pod ServiceAccount names; they become member-scoped (mck-member-<cluster>-*)
	// once that hardcoding is removed, at which point only these values change.
	workloadAppdbSA      string
	workloadDatabaseSA   string
	workloadOpsManagerSA string
	workloadAppdbRole    string
	workloadAppdbBinding string
}

func expectedNames(clusterName string) resourceNames {
	prefix := "mck-member-" + clusterName + "-"
	return resourceNames{
		operatorSA:      prefix + "sa",
		operatorToken:   prefix + "token",
		operatorRole:    prefix + "role",
		operatorBinding: prefix + "role-binding",

		workloadAppdbSA:      "mongodb-kubernetes-appdb",
		workloadDatabaseSA:   "mongodb-kubernetes-database-pods",
		workloadOpsManagerSA: "mongodb-kubernetes-ops-manager",
		workloadAppdbRole:    "mongodb-kubernetes-appdb",
		workloadAppdbBinding: "mongodb-kubernetes-appdb",
	}
}

// resourceID identifies a rendered resource by kind, name and namespace. It is the unit of
// comparison for the full-resource-set assertion in TestRender.
type resourceID struct {
	Kind      string
	Name      string
	Namespace string
}

// resourceIDs maps parsed objects into comparable resourceID values.
func resourceIDs(rs []*unstructured.Unstructured) []resourceID {
	out := make([]resourceID, 0, len(rs))
	for _, r := range rs {
		out = append(out, resourceID{Kind: r.GetKind(), Name: r.GetName(), Namespace: r.GetNamespace()})
	}
	return out
}

// workloadResources returns the fixed-name database-workload RBAC resources Render emits in
// each of the given namespaces: three ServiceAccounts, a Role and a RoleBinding.
func workloadResources(n resourceNames, namespaces ...string) []resourceID {
	var out []resourceID
	for _, ns := range namespaces {
		out = append(out,
			resourceID{Kind: "ServiceAccount", Name: n.workloadAppdbSA, Namespace: ns},
			resourceID{Kind: "ServiceAccount", Name: n.workloadDatabaseSA, Namespace: ns},
			resourceID{Kind: "ServiceAccount", Name: n.workloadOpsManagerSA, Namespace: ns},
			resourceID{Kind: "Role", Name: n.workloadAppdbRole, Namespace: ns},
			resourceID{Kind: "RoleBinding", Name: n.workloadAppdbBinding, Namespace: ns},
		)
	}
	return out
}

// TestRender asserts the full set of resources Render emits for each watched-namespaces
// scenario. watchedNamespaces drives both the operator role scope (Role vs ClusterRole) and
// the resources emitted. Note the asymmetry the expected sets encode: the operator bindings
// cover the union of the watched namespaces and the member namespace, while the workload
// resources follow only the watched namespaces ("*" mapping to the member namespace).
func TestRender(t *testing.T) {
	const clusterName = "cluster-east"
	const memberNs = "mongodb"
	n := expectedNames(clusterName)

	tests := []struct {
		name         string
		watched      []string
		wantRoleKind string
		want         []resourceID
	}{
		{
			name:         "single namespace equals member namespace",
			watched:      []string{memberNs},
			wantRoleKind: "Role",
			want: append([]resourceID{
				{Kind: "ServiceAccount", Name: n.operatorSA, Namespace: memberNs},
				{Kind: "Secret", Name: n.operatorToken, Namespace: memberNs},
				{Kind: "Role", Name: n.operatorRole, Namespace: memberNs},
				{Kind: "RoleBinding", Name: n.operatorBinding, Namespace: memberNs},
			}, workloadResources(n, memberNs)...),
		},
		{
			// A single watched namespace that differs from the member namespace unions to
			// {ns1, mongodb} (size 2), so the operator role becomes a ClusterRole with
			// RoleBindings in both namespaces, while the workload RBAC lands in ns1 only.
			name:         "single namespace differs from member namespace",
			watched:      []string{"ns1"},
			wantRoleKind: "ClusterRole",
			want: append([]resourceID{
				{Kind: "ServiceAccount", Name: n.operatorSA, Namespace: memberNs},
				{Kind: "Secret", Name: n.operatorToken, Namespace: memberNs},
				{Kind: "ClusterRole", Name: n.operatorRole, Namespace: ""},
				{Kind: "RoleBinding", Name: n.operatorBinding, Namespace: memberNs},
				{Kind: "RoleBinding", Name: n.operatorBinding, Namespace: "ns1"},
			}, workloadResources(n, "ns1")...),
		},
		{
			name:         "multiple namespaces",
			watched:      []string{"ns1", "ns2"},
			wantRoleKind: "ClusterRole",
			want: append([]resourceID{
				{Kind: "ServiceAccount", Name: n.operatorSA, Namespace: memberNs},
				{Kind: "Secret", Name: n.operatorToken, Namespace: memberNs},
				{Kind: "ClusterRole", Name: n.operatorRole, Namespace: ""},
				{Kind: "RoleBinding", Name: n.operatorBinding, Namespace: memberNs},
				{Kind: "RoleBinding", Name: n.operatorBinding, Namespace: "ns1"},
				{Kind: "RoleBinding", Name: n.operatorBinding, Namespace: "ns2"},
			}, workloadResources(n, "ns1", "ns2")...),
		},
		{
			name:         "all namespaces",
			watched:      []string{"*"},
			wantRoleKind: "ClusterRole",
			want: append([]resourceID{
				{Kind: "ServiceAccount", Name: n.operatorSA, Namespace: memberNs},
				{Kind: "Secret", Name: n.operatorToken, Namespace: memberNs},
				{Kind: "ClusterRole", Name: n.operatorRole, Namespace: ""},
				{Kind: "ClusterRoleBinding", Name: n.operatorBinding, Namespace: ""},
			}, workloadResources(n, memberNs)...),
		},
	}

	operatorNames := map[string]bool{
		n.operatorSA:      true,
		n.operatorToken:   true,
		n.operatorRole:    true,
		n.operatorBinding: true,
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, err := Render(clusterName, memberNs, tc.watched, "")
			require.NoError(t, err, "render failed")
			resources := parseResources(t, out)

			require.ElementsMatch(t, tc.want, resourceIDs(resources), "unexpected resources")

			for _, r := range resources {
				// Operator member resources carry the rbac-version annotation the operator's
				// RBAC validation relies on.
				if operatorNames[r.GetName()] {
					assert.NotEmpty(t, r.GetAnnotations()["mongodb.com/rbac-version"], "%s/%s missing mongodb.com/rbac-version annotation", r.GetKind(), r.GetName())
				}
				// The operator bindings point at the expected operator role scope (the workload
				// binding always references its own namespaced Role, so it is excluded here).
				if r.GetName() == n.operatorBinding && (r.GetKind() == "RoleBinding" || r.GetKind() == "ClusterRoleBinding") {
					roleRefKind, _, _ := unstructured.NestedString(r.Object, "roleRef", "kind")
					assert.Equal(t, tc.wantRoleKind, roleRefKind, "%s/%s roleRef.kind", r.GetKind(), r.GetName())
				}
				// Without an imagePullSecrets argument, no ServiceAccount should carry one.
				if r.GetKind() == "ServiceAccount" {
					_, found, _ := unstructured.NestedSlice(r.Object, "imagePullSecrets")
					assert.False(t, found, "%s/%s should have no imagePullSecrets by default", r.GetKind(), r.GetName())
				}
			}
		})
	}
}

func TestRender_RequiresClusterName(t *testing.T) {
	// Render itself renders whatever it is given; the required-name guard lives in the chart
	// template, so an empty cluster name must surface as a render error.
	_, err := Render("", "mongodb", []string{"mongodb"}, "")
	require.Error(t, err, "expected an error when the cluster name is empty")
}

// TestRender_ImagePullSecrets asserts that a non-empty imagePullSecrets argument is set on
// the workload ServiceAccounts only (the operator's own member SA carries no image pull
// secret, since it is not used to pull workload images).
func TestRender_ImagePullSecrets(t *testing.T) {
	const clusterName = "cluster-east"
	const memberNs = "mongodb"
	n := expectedNames(clusterName)

	out, err := Render(clusterName, memberNs, []string{memberNs}, "my-pull-secret")
	require.NoError(t, err, "render failed")
	resources := parseResources(t, out)

	workloadSAs := map[string]bool{
		n.workloadAppdbSA:      true,
		n.workloadDatabaseSA:   true,
		n.workloadOpsManagerSA: true,
	}

	var sawWorkloadSA int
	for _, r := range resources {
		if r.GetKind() != "ServiceAccount" {
			continue
		}
		pullSecrets, _, err := unstructured.NestedSlice(r.Object, "imagePullSecrets")
		require.NoError(t, err, "%s/%s reading imagePullSecrets", r.GetKind(), r.GetName())

		if workloadSAs[r.GetName()] {
			sawWorkloadSA++
			require.Len(t, pullSecrets, 1, "%s/%s imagePullSecrets", r.GetKind(), r.GetName())
			name, _, _ := unstructured.NestedString(pullSecrets[0].(map[string]any), "name")
			assert.Equal(t, "my-pull-secret", name, "%s/%s imagePullSecrets[0].name", r.GetKind(), r.GetName())
		} else if r.GetName() == n.operatorSA {
			assert.Empty(t, pullSecrets, "%s/%s should have no imagePullSecrets", r.GetKind(), r.GetName())
		}
	}
	assert.Equal(t, len(workloadSAs), sawWorkloadSA, "expected to see all workload ServiceAccounts")
}

// TestEmbeddedChartMatchesDisk is the drift guard: every file the plugin embeds must
// match the on-disk chart byte-for-byte, and every chart file we intend to embed
// (templates/, crds/, Chart.yaml, values.yaml) must actually be embedded. This is
// what catches a new chart file slipping past the //go:embed pattern.
func TestEmbeddedChartMatchesDisk(t *testing.T) {
	// 1. Embedded files must exist on disk with identical content.
	embedded := map[string]bool{}
	err := fs.WalkDir(helmchart.ChartFiles, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		embedded[p] = true
		want, err := os.ReadFile(filepath.Join(diskChartDir, p))
		if err != nil {
			assert.NoError(t, err, "embedded file %q missing on disk", p)
			return nil
		}
		got, err := helmchart.ChartFiles.ReadFile(p)
		if err != nil {
			return err
		}
		assert.Equal(t, string(want), string(got), "embedded file %q differs from on-disk chart", p)
		return nil
	})
	require.NoError(t, err, "walking embedded chart")

	// 2. Chart files we intend to embed must all be present in the embedded FS.
	roots := []string{"Chart.yaml", "values.yaml", "templates", "crds"}
	for _, root := range roots {
		diskRoot := filepath.Join(diskChartDir, root)
		_ = filepath.WalkDir(diskRoot, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			rel, _ := filepath.Rel(diskChartDir, p)
			rel = filepath.ToSlash(rel)
			assert.Contains(t, embedded, rel, "chart file %q is on disk but not embedded — update the //go:embed pattern in helm_chart/embed.go", rel)
			return nil
		})
	}
}

// TestHelmTemplateParity cross-checks the embedded render against the `helm` CLI
// rendering the on-disk chart, as a belt-and-braces check that our SDK rendering
// matches real Helm.
func TestHelmTemplateParity(t *testing.T) {
	helmBin, err := exec.LookPath("helm")
	require.NoError(t, err, "helm must be installed to run the chart parity test")

	cmd := exec.Command(helmBin, "template", diskChartDir,
		"--set", "memberCluster.enabled=true",
		"--set", "memberCluster.name=cluster-east",
		"--set", "operator.namespace=mongodb",
		"--show-only", "templates/member-cluster-rbac.yaml",
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	require.NoError(t, cmd.Run(), "helm template failed\n%s", stderr.String())

	const clusterName = "cluster-east"
	names := expectedNames(clusterName)

	embeddedOut, err := Render(clusterName, "mongodb", []string{"mongodb"}, "")
	require.NoError(t, err, "embedded render failed")

	helmResources := kindNames(parseResources(t, stdout.String()))
	// The CLI --show-only call renders just the member template, so restrict the embedded
	// output to the operator member resources (by name) before comparing.
	operatorNames := map[string]bool{
		names.operatorSA:      true,
		names.operatorToken:   true,
		names.operatorRole:    true,
		names.operatorBinding: true,
	}
	var embeddedMember []*unstructured.Unstructured
	for _, r := range parseResources(t, embeddedOut) {
		if operatorNames[r.GetName()] {
			embeddedMember = append(embeddedMember, r)
		}
	}
	embeddedNames := kindNames(embeddedMember)
	require.ElementsMatch(t, helmResources, embeddedNames, "embedded render and helm CLI disagree on member resources")
}
