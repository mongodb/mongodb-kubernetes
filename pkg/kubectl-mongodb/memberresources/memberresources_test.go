package memberresources

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
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
	// Operator member RBAC, named mck-member-*: three roles,
	// each with its own binding(s). role-base (the operator's shared
	// workload-management rules, operator-roles-base.yaml), role-multicluster (rules
	// needed only for multi-cluster operation, member-cluster-rbac.yaml) and
	// pvc-resize (operator-roles-pvc-resize.yaml).
	operatorSA              string
	operatorToken           string
	roleBase                string
	roleBaseBinding         string
	roleMulticluster        string
	roleMulticlusterBinding string
	pvcResizeRole           string
	pvcResizeBinding        string

	// Telemetry RBAC, rendered by the dual-mode operator-roles-telemetry.yaml unless
	// operatorTelemetry is false.
	telemetryClusterRole string
	telemetryBinding     string

	// Database-workload RBAC, also named mck-member-* in
	// member mode so it is additive to the base installation's fixed-name workload RBAC.
	workloadAppdbSA      string
	workloadDatabaseSA   string
	workloadOpsManagerSA string
	workloadAppdbRole    string
	workloadAppdbBinding string
}

func expectedNames() resourceNames {
	const prefix = "mck-member-"
	return resourceNames{
		operatorSA:              prefix + "sa",
		operatorToken:           prefix + "token",
		roleBase:                prefix + "role-base",
		roleBaseBinding:         prefix + "role-base-binding",
		roleMulticluster:        prefix + "role-multicluster",
		roleMulticlusterBinding: prefix + "role-multicluster-binding",
		pvcResizeRole:           prefix + "pvc-resize",
		pvcResizeBinding:        prefix + "pvc-resize-binding",

		telemetryClusterRole: prefix + "cluster-telemetry",
		telemetryBinding:     prefix + "cluster-telemetry-binding",

		workloadAppdbSA:      prefix + "appdb",
		workloadDatabaseSA:   prefix + "database-pods",
		workloadOpsManagerSA: prefix + "ops-manager",
		workloadAppdbRole:    prefix + "appdb",
		workloadAppdbBinding: prefix + "appdb",
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

// telemetryResources returns the telemetry ClusterRole/ClusterRoleBinding rendered by the
// dual-mode operator-roles-telemetry.yaml (both cluster-scoped, hence no namespace).
func telemetryResources(n resourceNames) []resourceID {
	return []resourceID{
		{Kind: "ClusterRole", Name: n.telemetryClusterRole, Namespace: ""},
		{Kind: "ClusterRoleBinding", Name: n.telemetryBinding, Namespace: ""},
	}
}

// operatorResources returns the resourceIDs of the three operator member roles
// (role-base, role-multicluster, pvc-resize) and their bindings for the given scope. The
// three roles always share the same scope: roleKind is "Role" (narrowed to the single
// member namespace) or "ClusterRole" (multi-namespace narrowed or cluster-scoped). In
// Role mode the roles and one binding each live in namespaces[0]; in ClusterRole mode the
// roles are cluster-scoped and one RoleBinding each lands in every given namespace — a
// single empty namespace denotes the cluster-scoped ClusterRoleBinding instead.
func operatorResources(n resourceNames, roleKind string, namespaces ...string) []resourceID {
	roleNamespace := ""
	if roleKind == "Role" {
		roleNamespace = namespaces[0]
	}
	roles := []struct{ role, binding string }{
		{n.roleMulticluster, n.roleMulticlusterBinding},
		{n.roleBase, n.roleBaseBinding},
		{n.pvcResizeRole, n.pvcResizeBinding},
	}
	var out []resourceID
	for _, r := range roles {
		out = append(out, resourceID{Kind: roleKind, Name: r.role, Namespace: roleNamespace})
		for _, ns := range namespaces {
			bindingKind := "RoleBinding"
			if ns == "" {
				bindingKind = "ClusterRoleBinding"
			}
			out = append(out, resourceID{Kind: bindingKind, Name: r.binding, Namespace: ns})
		}
	}
	return out
}

// TestRender asserts the full set of resources Render emits for each flag combination.
// workloadNamespaces drives workload RBAC placement and (in narrowed mode) the member SA's
// per-namespace bindings; operatorClusterScoped switches the member SA to a single ClusterRoleBinding.
// Note the asymmetry the expected sets encode: in narrowed mode the operator bindings cover
// the union of the workload namespaces and the member namespace, while the workload resources
// follow only the workload namespaces. Telemetry resources are present unless
// operatorTelemetry is false.
func TestRender(t *testing.T) {
	const memberNs = "mongodb"
	n := expectedNames()

	tests := []struct {
		name                  string
		workload              []string
		operatorClusterScoped bool
		operatorTelemetry     bool
		wantRoleKind          string
		want                  []resourceID
	}{
		{
			name:              "defaults: workload namespace equals member namespace",
			workload:          []string{memberNs},
			operatorTelemetry: true,
			wantRoleKind:      "Role",
			want: append(append(append([]resourceID{
				{Kind: "ServiceAccount", Name: n.operatorSA, Namespace: memberNs},
				{Kind: "Secret", Name: n.operatorToken, Namespace: memberNs},
			}, operatorResources(n, "Role", memberNs)...), telemetryResources(n)...), workloadResources(n, memberNs)...),
		},
		{
			// A single workload namespace that differs from the member namespace unions to
			// {ns1, mongodb} (size 2), so the operator roles become ClusterRoles with
			// RoleBindings in both namespaces, while the workload RBAC lands in ns1 only.
			name:              "single workload namespace differs from member namespace",
			workload:          []string{"ns1"},
			operatorTelemetry: true,
			wantRoleKind:      "ClusterRole",
			want: append(append(append([]resourceID{
				{Kind: "ServiceAccount", Name: n.operatorSA, Namespace: memberNs},
				{Kind: "Secret", Name: n.operatorToken, Namespace: memberNs},
			}, operatorResources(n, "ClusterRole", memberNs, "ns1")...), telemetryResources(n)...), workloadResources(n, "ns1")...),
		},
		{
			name:              "multiple workload namespaces",
			workload:          []string{"ns1", "ns2"},
			operatorTelemetry: true,
			wantRoleKind:      "ClusterRole",
			want: append(append(append([]resourceID{
				{Kind: "ServiceAccount", Name: n.operatorSA, Namespace: memberNs},
				{Kind: "Secret", Name: n.operatorToken, Namespace: memberNs},
			}, operatorResources(n, "ClusterRole", memberNs, "ns1", "ns2")...), telemetryResources(n)...), workloadResources(n, "ns1", "ns2")...),
		},
		{
			name:                  "cluster-scoped with default workload namespaces",
			workload:              []string{memberNs},
			operatorClusterScoped: true,
			operatorTelemetry:     true,
			wantRoleKind:          "ClusterRole",
			want: append(append(append([]resourceID{
				{Kind: "ServiceAccount", Name: n.operatorSA, Namespace: memberNs},
				{Kind: "Secret", Name: n.operatorToken, Namespace: memberNs},
			}, operatorResources(n, "ClusterRole", "")...), telemetryResources(n)...), workloadResources(n, memberNs)...),
		},
		{
			name:                  "cluster-scoped with explicit workload namespaces",
			workload:              []string{"ns1", "ns2"},
			operatorClusterScoped: true,
			operatorTelemetry:     true,
			wantRoleKind:          "ClusterRole",
			want: append(append(append([]resourceID{
				{Kind: "ServiceAccount", Name: n.operatorSA, Namespace: memberNs},
				{Kind: "Secret", Name: n.operatorToken, Namespace: memberNs},
			}, operatorResources(n, "ClusterRole", "")...), telemetryResources(n)...), workloadResources(n, "ns1", "ns2")...),
		},
		{
			name:              "telemetry roles opted out",
			workload:          []string{memberNs},
			operatorTelemetry: false,
			wantRoleKind:      "Role",
			want: append(append([]resourceID{
				{Kind: "ServiceAccount", Name: n.operatorSA, Namespace: memberNs},
				{Kind: "Secret", Name: n.operatorToken, Namespace: memberNs},
			}, operatorResources(n, "Role", memberNs)...), workloadResources(n, memberNs)...),
		},
	}

	// The three operator bindings (role-base, role-multicluster, pvc-resize) all share the
	// same scope in every test case and must point at a role of wantRoleKind.
	operatorBindings := map[string]bool{
		n.roleBaseBinding:         true,
		n.roleMulticlusterBinding: true,
		n.pvcResizeBinding:        true,
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, err := Render(memberNs, tc.workload, tc.operatorClusterScoped, tc.operatorTelemetry, "")
			require.NoError(t, err, "render failed")
			resources := parseResources(t, out)

			require.ElementsMatch(t, tc.want, resourceIDs(resources), "unexpected resources")

			for _, r := range resources {
				// Every member-mode resource — operator, telemetry and workload alike — carries
				// the rbac-version annotation the operator's RBAC validation relies on.
				assert.NotEmpty(t, r.GetAnnotations()["mongodb.com/rbac-version"], "%s/%s missing mongodb.com/rbac-version annotation", r.GetKind(), r.GetName())
				// The operator bindings point at the expected operator role scope (the workload
				// binding always references its own namespaced Role, so it is excluded here).
				if operatorBindings[r.GetName()] && (r.GetKind() == "RoleBinding" || r.GetKind() == "ClusterRoleBinding") {
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

// rbacRule is a normalised view of a single RBAC policy rule; all slices are sorted so
// rules compare equal regardless of the order the template lists them in.
type rbacRule struct {
	apiGroups []string
	resources []string
	verbs     []string
}

func sortedStringField(t *testing.T, rule map[string]any, field string) []string {
	t.Helper()
	values, found, err := unstructured.NestedStringSlice(rule, field)
	require.NoError(t, err, "reading rule field %q", field)
	require.True(t, found, "rule missing field %q", field)
	sort.Strings(values)
	return values
}

// rulesOf extracts the policy rules of a Role/ClusterRole as sorted rbacRule values.
func rulesOf(t *testing.T, r *unstructured.Unstructured) []rbacRule {
	t.Helper()
	raw, found, err := unstructured.NestedSlice(r.Object, "rules")
	require.NoError(t, err, "%s/%s reading rules", r.GetKind(), r.GetName())
	require.True(t, found, "%s/%s has no rules", r.GetKind(), r.GetName())
	out := make([]rbacRule, 0, len(raw))
	for _, item := range raw {
		rule, ok := item.(map[string]any)
		require.True(t, ok, "%s/%s rule is not a map", r.GetKind(), r.GetName())
		out = append(out, rbacRule{
			apiGroups: sortedStringField(t, rule, "apiGroups"),
			resources: sortedStringField(t, rule, "resources"),
			verbs:     sortedStringField(t, rule, "verbs"),
		})
	}
	return out
}

// TestRender_OperatorRoleRules pins the exact rule content of the three operator member
// roles: role-multicluster holds only the multi-cluster-only rules, role-base holds
// exactly the shared workload-management rules (no central-only rules), and pvc-resize
// holds its single least-privilege rule.
func TestRender_OperatorRoleRules(t *testing.T) {
	const memberNs = "mongodb"
	n := expectedNames()

	out, err := Render(memberNs, []string{memberNs}, false, true, "")
	require.NoError(t, err, "render failed")

	byName := map[string]*unstructured.Unstructured{}
	for _, r := range parseResources(t, out) {
		byName[r.GetName()] = r
	}
	for _, name := range []string{n.roleMulticluster, n.roleBase, n.pvcResizeRole} {
		require.Contains(t, byName, name, "rendered resources missing %s", name)
	}

	assert.ElementsMatch(t, []rbacRule{
		{apiGroups: []string{""}, resources: []string{"configmaps", "secrets", "services"}, verbs: []string{"deletecollection"}},
		{apiGroups: []string{"apps"}, resources: []string{"deployments", "statefulsets"}, verbs: []string{"deletecollection"}},
	}, rulesOf(t, byName[n.roleMulticluster]), "role-multicluster rules")

	baseRules := rulesOf(t, byName[n.roleBase])
	assert.ElementsMatch(t, []rbacRule{
		{apiGroups: []string{""}, resources: []string{"services"}, verbs: []string{"create", "delete", "get", "list", "update", "watch"}},
		{apiGroups: []string{""}, resources: []string{"configmaps", "secrets"}, verbs: []string{"create", "delete", "get", "list", "update", "watch"}},
		{apiGroups: []string{"apps"}, resources: []string{"deployments", "statefulsets"}, verbs: []string{"create", "delete", "get", "list", "update", "watch"}},
		{apiGroups: []string{""}, resources: []string{"pods"}, verbs: []string{"delete", "deletecollection", "get", "list", "watch"}},
	}, baseRules, "role-base rules")

	// Belt-and-braces alongside the exact-match above: role-base must not leak any of the
	// central-only rules the base installation's operator role carries.
	centralOnlyGroups := map[string]bool{
		"mongodb.com":                  true,
		"mongodbcommunity.mongodb.com": true,
		"ai.mongodb.com":               true,
		"operator.mongodb.com":         true,
	}
	for _, rule := range baseRules {
		for _, group := range rule.apiGroups {
			assert.False(t, centralOnlyGroups[group], "role-base must not contain central-only apiGroup %q", group)
		}
		assert.NotContains(t, rule.resources, "namespaces", "role-base must not contain the central-only namespaces rule")
	}

	assert.ElementsMatch(t, []rbacRule{
		{apiGroups: []string{""}, resources: []string{"persistentvolumeclaims"}, verbs: []string{"list", "update", "watch"}},
	}, rulesOf(t, byName[n.pvcResizeRole]), "pvc-resize rules")
}

// TestRender_RejectsWildcard asserts the chart-level backstop for "*": the CLI rejects it
// when parsing --workload-namespaces, but the member-mode templates must also refuse it,
// since "*" is only ever valid via operatorClusterScoped.
func TestRender_RejectsWildcard(t *testing.T) {
	for _, workload := range [][]string{{"*"}, {"ns1", "*"}} {
		_, err := Render("mongodb", workload, false, true, "")
		require.Error(t, err, "expected an error for workload namespaces %v", workload)
		assert.Contains(t, err.Error(), "--operator-cluster-scoped", "error should point at --operator-cluster-scoped, got: %v", err)
	}
}

// TestRender_ImagePullSecrets asserts that a non-empty imagePullSecrets argument is set on
// the workload ServiceAccounts only (the operator's own member SA carries no image pull
// secret, since it is not used to pull workload images).
func TestRender_ImagePullSecrets(t *testing.T) {
	const memberNs = "mongodb"
	n := expectedNames()

	out, err := Render(memberNs, []string{memberNs}, false, true, "my-pull-secret")
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
// matches real Helm. Each template Render embeds is rendered via the CLI with
// --show-only and compared against the corresponding documents of the embedded render.
func TestHelmTemplateParity(t *testing.T) {
	helmBin, err := exec.LookPath("helm")
	require.NoError(t, err, "helm must be installed to run the chart parity test")

	names := expectedNames()

	helmTemplate := func(showOnly string) string {
		cmd := exec.Command(helmBin, "template", diskChartDir,
			"--set", "memberCluster.enabled=true",
			"--set", "memberCluster.clusterScoped=false",
			"--set", "memberCluster.workloadNamespaces[0]=mongodb",
			"--set", "operator.namespace=mongodb",
			"--set", "operator.telemetry.installClusterRole=true",
			"--show-only", showOnly,
		)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		require.NoError(t, cmd.Run(), "helm template failed\n%s", stderr.String())
		return stdout.String()
	}

	embeddedOut, err := Render("mongodb", []string{"mongodb"}, false, true, "")
	require.NoError(t, err, "embedded render failed")
	embeddedResources := parseResources(t, embeddedOut)

	// Each CLI --show-only call renders a single template, so restrict the embedded
	// output to that template's resources (by name) before comparing.
	for _, tc := range []struct {
		template string
		names    map[string]bool
	}{
		{
			template: "templates/member-cluster-rbac.yaml",
			names: map[string]bool{
				names.operatorSA:              true,
				names.operatorToken:           true,
				names.roleMulticluster:        true,
				names.roleMulticlusterBinding: true,
			},
		},
		{
			template: "templates/operator-roles-base.yaml",
			names: map[string]bool{
				names.roleBase:        true,
				names.roleBaseBinding: true,
			},
		},
		{
			template: "templates/operator-roles-pvc-resize.yaml",
			names: map[string]bool{
				names.pvcResizeRole:    true,
				names.pvcResizeBinding: true,
			},
		},
		{
			template: "templates/database-roles.yaml",
			names: map[string]bool{
				names.workloadAppdbSA:      true,
				names.workloadDatabaseSA:   true,
				names.workloadOpsManagerSA: true,
			},
		},
		{
			template: "templates/operator-roles-telemetry.yaml",
			names: map[string]bool{
				names.telemetryClusterRole: true,
				names.telemetryBinding:     true,
			},
		},
	} {
		t.Run(tc.template, func(t *testing.T) {
			helmResources := kindNames(parseResources(t, helmTemplate(tc.template)))
			var embeddedSubset []*unstructured.Unstructured
			for _, r := range embeddedResources {
				if tc.names[r.GetName()] {
					embeddedSubset = append(embeddedSubset, r)
				}
			}
			require.ElementsMatch(t, helmResources, kindNames(embeddedSubset), "embedded render and helm CLI disagree on %s resources", tc.template)
		})
	}
}
