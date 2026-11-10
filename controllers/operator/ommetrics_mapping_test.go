package operator

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	mdbmulti "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdbmulti"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/om/deployment"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	"github.com/mongodb/mongodb-kubernetes/pkg/images"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/ommetrics"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/architectures"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/versionutil"
)

type recordedMonitoringMapping struct {
	calls       int
	project     ommetrics.Project
	agentAPIKey string
	tlsOpts     ommetrics.TLSOptions
	deployment  ommetrics.MongoDBDeployment
	released    []ommetrics.Project
	// ops records ensure/release calls in order.
	ops []string
}

func (r *recordedMonitoringMapping) EnsureProject(p ommetrics.Project, agentAPIKey string, tlsOpts ommetrics.TLSOptions, deployment ommetrics.MongoDBDeployment) error {
	r.calls++
	r.project, r.agentAPIKey, r.tlsOpts, r.deployment = p, agentAPIKey, tlsOpts, deployment
	r.ops = append(r.ops, "ensure "+p.BaseURL+"/"+p.GroupID)
	return nil
}

func (r *recordedMonitoringMapping) ReleaseProject(p ommetrics.Project) error {
	r.released = append(r.released, p)
	r.ops = append(r.ops, "release "+p.BaseURL+"/"+p.GroupID)
	return nil
}

func installMonitoringMappingRecorder(common *ReconcileCommonController) *recordedMonitoringMapping {
	recorder := &recordedMonitoringMapping{}
	common.omMetrics = recorder
	return recorder
}

func requireMonitoringMapping(t *testing.T, recorder *recordedMonitoringMapping, deploymentName string, uid string) {
	t.Helper()
	require.NotNil(t, recorder.deployment)
	require.Equal(t, deploymentName, recorder.deployment.OpsManagerDeploymentName())
	require.Equal(t, uid, recorder.deployment.TelemetryIdentifier())
}

type fakeDeployment struct{ uid, name string }

func (d fakeDeployment) TelemetryIdentifier() string      { return d.uid }
func (d fakeDeployment) OpsManagerDeploymentName() string { return d.name }

// monitoringMappingConnectionFactory mocks an Ops Manager version that supports OTLP metrics.
func monitoringMappingConnectionFactory() *om.CachedOMConnectionFactory {
	return om.NewCachedOMConnectionFactory(func(ctx *om.OMContext) om.Connection {
		ctx.Version = versionutil.OpsManagerVersion{VersionString: versionutil.MinOTLPMetricsOpsManagerVersion}
		return om.NewEmptyMockedOmConnection(ctx)
	})
}

func runReconcileAndRequireMonitoringMapping(ctx context.Context, t *testing.T, reconciler reconcile.Reconciler, commonController *ReconcileCommonController, kubeClient kubernetesClient.Client, object *mdbv1.MongoDB, deploymentName string) {
	t.Helper()
	recorder := installMonitoringMappingRecorder(commonController)

	checkReconcileSuccessful(ctx, t, reconciler, object, kubeClient)

	requireMonitoringMapping(t, recorder, deploymentName, string(object.UID))
	require.Equal(t, []string{"ensure " + om.TestURL + "/" + om.TestGroupID}, recorder.ops)

	// Project switch: the status still records the previous project until the reconcile succeeds.
	recorder.ops = nil
	setPreviousStatusLink(ctx, t, kubeClient, object)

	checkReconcileSuccessful(ctx, t, reconciler, object, kubeClient)

	require.Equal(t, []string{"release " + previousProjectLinkBase, "ensure " + om.TestURL + "/" + om.TestGroupID}, recorder.ops)
}

func setPreviousStatusLink(ctx context.Context, t *testing.T, kubeClient kubernetesClient.Client, object *mdbv1.MongoDB) {
	t.Helper()
	object.Status.Link = previousProjectLink
	require.NoError(t, kubeClient.Status().Update(ctx, object))
	if object.Spec.ResourceType != mdbv1.ShardedCluster {
		return
	}
	// Sharded clusters restore their status from the deployment state, which takes precedence over the CR.
	store := NewStateStore[ShardedClusterDeploymentState](object, kube.BaseOwnerReference(object), kubeClient)
	state, err := store.ReadState(ctx)
	require.NoError(t, err)
	state.Status.Link = previousProjectLink
	require.NoError(t, store.WriteState(ctx, state, zap.S()))
}

func runMultiReconcileAndRequireMonitoringMapping(ctx context.Context, t *testing.T, reconciler reconcile.Reconciler, commonController *ReconcileCommonController, kubeClient kubernetesClient.Client, object *mdbmulti.MongoDBMultiCluster, deploymentName string) {
	t.Helper()
	recorder := installMonitoringMappingRecorder(commonController)

	checkMultiReconcileSuccessful(ctx, t, reconciler, object, kubeClient, false)

	requireMonitoringMapping(t, recorder, deploymentName, string(object.UID))
	require.Equal(t, []string{"ensure " + om.TestURL + "/" + om.TestGroupID}, recorder.ops)
	require.NoError(t, kubeClient.Get(ctx, mock.ObjectKeyFromApiObject(object), object))
	require.Equal(t, deployment.Link(om.TestURL, om.TestGroupID), object.Status.Link)

	recorder.ops = nil
	object.Status.Link = previousProjectLink
	require.NoError(t, kubeClient.Status().Update(ctx, object))

	checkMultiReconcileSuccessful(ctx, t, reconciler, object, kubeClient, false)

	require.Equal(t, []string{"release " + previousProjectLinkBase, "ensure " + om.TestURL + "/" + om.TestGroupID}, recorder.ops)
}

const (
	previousProjectLinkBase = "https://previous-om.example.com/previous-group"
	previousProjectLink     = "https://previous-om.example.com/v2/previous-group"
)

func TestEnsureOtelExporterForDeployment_ReleasesPreviousProject(t *testing.T) {
	const current = "https://om.example.com/g1"
	tests := map[string]struct {
		previousLink string
		version      string
		wantOps      []string
	}{
		"first reconcile":       {previousLink: "", wantOps: []string{"ensure " + current}},
		"same project":          {previousLink: "https://om.example.com/v2/g1", wantOps: []string{"ensure " + current}},
		"same project spelling": {previousLink: "HTTPS://OM.example.com:443/v2/g1", wantOps: []string{"ensure " + current}},
		"unparseable link":      {previousLink: "not-a-link", wantOps: []string{"ensure " + current}},
		"switched project":      {previousLink: "https://om.example.com/v2/g0", wantOps: []string{"release https://om.example.com/g0", "ensure " + current}},
		"switched Ops Manager":  {previousLink: "https://old-om.example.com/v2/g1", wantOps: []string{"release https://old-om.example.com/g1", "ensure " + current}},
		"release is not gated":  {previousLink: "https://om.example.com/v2/g0", version: "v20250101", wantOps: []string{"release https://om.example.com/g0"}},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			version := tc.version
			if version == "" {
				version = versionutil.MinOTLPMetricsOpsManagerVersion
			}
			common := &ReconcileCommonController{}
			recorder := installMonitoringMappingRecorder(common)
			conn := om.NewEmptyMockedOmConnection(&om.OMContext{
				BaseURL: "https://om.example.com",
				GroupID: "g1",
				Version: versionutil.OpsManagerVersion{VersionString: version},
			})

			common.EnsureOtelExporterForDeployment(conn, "agent-key", tc.previousLink, fakeDeployment{uid: "uid-1", name: "rs"}, zap.S())

			require.Equal(t, tc.wantOps, recorder.ops)
		})
	}
}

func TestEnsureOtelExporterForDeployment_Gates(t *testing.T) {
	tests := map[string]struct {
		version      string
		tlsOpts      ommetrics.TLSOptions
		wantRegister bool
	}{
		"cloud manager":        {version: "v20250101", wantRegister: false},
		"unknown version":      {version: "", wantRegister: false},
		"below minimum":        {version: "8.0.24.500.20250101T0000Z", wantRegister: false},
		"supported":            {version: versionutil.MinOTLPMetricsOpsManagerVersion, wantRegister: true},
		"newer than supported": {version: "8.1.0.100.20250101T0000Z", wantRegister: true},
		"TLS settings forwarded": {
			version:      versionutil.MinOTLPMetricsOpsManagerVersion,
			tlsOpts:      ommetrics.TLSOptions{CACertificate: "ca-pem", AllowInvalidSSLCertificate: true},
			wantRegister: true,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			common := &ReconcileCommonController{}
			recorder := installMonitoringMappingRecorder(common)
			conn := om.NewEmptyMockedOmConnection(&om.OMContext{
				BaseURL:                    "https://om.example.com",
				GroupID:                    "g1",
				Version:                    versionutil.OpsManagerVersion{VersionString: tc.version},
				CACertificate:              tc.tlsOpts.CACertificate,
				AllowInvalidSSLCertificate: tc.tlsOpts.AllowInvalidSSLCertificate,
			})

			common.EnsureOtelExporterForDeployment(conn, "agent-key", "", fakeDeployment{uid: "uid-1", name: "rs"}, zap.S())

			if !tc.wantRegister {
				require.Zero(t, recorder.calls)
				return
			}
			require.Equal(t, 1, recorder.calls)
			require.Equal(t, ommetrics.Project{BaseURL: "https://om.example.com", GroupID: "g1"}, recorder.project)
			require.Equal(t, "agent-key", recorder.agentAPIKey)
			require.Equal(t, tc.tlsOpts, recorder.tlsOpts)
			requireMonitoringMapping(t, recorder, "rs", "uid-1")
		})
	}
}

func TestOtelExporter_NilTelemetryIsSafe(t *testing.T) {
	ctx := context.Background()
	kubeClient, _ := mock.NewDefaultFakeClient()
	common := NewReconcileCommonController(ctx, kubeClient, nil)
	require.Nil(t, common.omMetrics)

	conn := om.NewEmptyMockedOmConnection(&om.OMContext{
		BaseURL: "https://om.example.com",
		GroupID: "g1",
		Version: versionutil.OpsManagerVersion{VersionString: versionutil.MinOTLPMetricsOpsManagerVersion},
	})
	require.NotPanics(t, func() {
		common.EnsureOtelExporterForDeployment(conn, "agent-key", "https://om.example.com/v2/g0", fakeDeployment{uid: "uid-1", name: "rs"}, zap.S())
		common.ReleaseOtelExporter("https://om.example.com/v2/g1", zap.S())
	})
}

func TestReleaseOtelExporter_UsesStatusLink(t *testing.T) {
	common := &ReconcileCommonController{}
	recorder := installMonitoringMappingRecorder(common)

	common.ReleaseOtelExporter("https://om.example.com/v2/g1", zap.S())
	common.ReleaseOtelExporter("", zap.S())
	common.ReleaseOtelExporter("https://om.example.com/g1", zap.S())

	require.Equal(t, []ommetrics.Project{{BaseURL: "https://om.example.com", GroupID: "g1"}}, recorder.released)
}

func TestReplicaSetReconcile_ExportsEvenWhenLaterStepsFail(t *testing.T) {
	ctx := context.Background()
	rs := DefaultReplicaSetBuilder().Build()
	rs.SetUID(types.UID("replica-set-uid"))
	rs.Status.Link = previousProjectLink

	omConnectionFactory := monitoringMappingConnectionFactory()
	kubeClient := mock.NewEmptyFakeClientBuilder().WithObjects(rs).WithObjects(mock.GetDefaultResources()...).Build()
	// The StatefulSet never becomes ready, so the reconcile ends Pending after the Ensure point.
	kubeClient = interceptor.NewClient(kubeClient, interceptor.Funcs{
		Get: mock.GetFakeClientInterceptorGetFunc(omConnectionFactory, false, false),
	})
	recorder := &recordedMonitoringMapping{}
	reconciler := newReplicaSetReconciler(ctx, kubeClient, nil, "", "", false, false, false, "", architectures.NonStatic, omConnectionFactory.GetConnectionFunc, recorder)

	checkReconcilePending(ctx, t, reconciler, rs, "StatefulSet not ready", kubeClient, 3)

	require.Equal(t, []string{"release " + previousProjectLinkBase, "ensure " + om.TestURL + "/" + om.TestGroupID}, recorder.ops)
	requireMonitoringMapping(t, recorder, rs.GetReplicaSetName(), string(rs.UID))
	// Pending reconciles leave status.link untouched; the next reconcile releases the (already gone) previous project again.
	require.Equal(t, previousProjectLink, rs.Status.Link)
}

func TestReplicaSetReconcile_NoExportWhenFailingBeforeProjectSwitch(t *testing.T) {
	ctx := context.Background()
	rs := DefaultReplicaSetBuilder().SetReplicaSetHorizons([]mdbv1.MongoDBHorizonConfig{
		{"my-horizon": "my-db.com:12345"},
		{"my-horizon": "my-db.com:12346"},
		{"my-horizon": "my-db.com:12347"},
	}).Build()
	reconciler, kubeClient, _ := defaultReplicaSetReconcilerWithConnectionFactory(ctx, nil, "", "", rs, architectures.NonStatic, monitoringMappingConnectionFactory())
	recorder := installMonitoringMappingRecorder(reconciler.ReconcileCommonController)
	setPreviousStatusLink(ctx, t, kubeClient, rs)

	checkReconcileFailed(ctx, t, reconciler, rs, false, "TLS must be enabled in order to use replica set horizons", kubeClient)

	require.Empty(t, recorder.ops)
}

func TestReplicaSetReconcile_NoExportDuringMigrationDryRun(t *testing.T) {
	ctx := context.Background()
	rs := DefaultReplicaSetBuilder().Build()
	rs.SetUID(types.UID("replica-set-uid"))
	rs.Annotations = map[string]string{util.MigrationDryRunAnnotation: "true"}
	reconciler, kubeClient, _ := defaultReplicaSetReconcilerWithConnectionFactory(ctx, images.ImageUrls{util.OperatorImageEnv: "quay.io/mongodb/mongodb-kubernetes:test"}, "", "", rs, architectures.NonStatic, monitoringMappingConnectionFactory())
	recorder := installMonitoringMappingRecorder(reconciler.ReconcileCommonController)

	_, err := reconciler.Reconcile(ctx, requestFromObject(rs))
	require.NoError(t, err)
	require.NoError(t, kubeClient.Get(ctx, mock.ObjectKeyFromApiObject(rs), rs))
	require.Equal(t, status.PhaseConnectivityValidation, rs.Status.Phase)
	require.Empty(t, recorder.ops)

	// Leaving dry-run starts the export.
	delete(rs.Annotations, util.MigrationDryRunAnnotation)
	checkReconcileSuccessful(ctx, t, reconciler, rs, kubeClient)

	require.Equal(t, []string{"ensure " + om.TestURL + "/" + om.TestGroupID}, recorder.ops)
	requireMonitoringMapping(t, recorder, rs.GetReplicaSetName(), string(rs.UID))
}
