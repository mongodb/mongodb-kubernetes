package operator

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	mdbmulti "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdbmulti"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/ommetrics"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/versionutil"
)

type recordedMonitoringMapping struct {
	calls       int
	project     ommetrics.Project
	agentAPIKey string
	tlsOpts     ommetrics.TLSOptions
	deployment  ommetrics.Deployment
	released    []ommetrics.Project
}

func (r *recordedMonitoringMapping) EnsureProject(p ommetrics.Project, agentAPIKey string, tlsOpts ommetrics.TLSOptions, deployment ommetrics.Deployment) error {
	r.calls++
	r.project, r.agentAPIKey, r.tlsOpts, r.deployment = p, agentAPIKey, tlsOpts, deployment
	return nil
}

func (r *recordedMonitoringMapping) ReleaseProject(p ommetrics.Project) error {
	r.released = append(r.released, p)
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
	require.Equal(t, deploymentName, recorder.deployment.AutomationDeploymentName())
	require.Equal(t, uid, recorder.deployment.TelemetryIdentifier())
}

type fakeDeployment struct{ uid, name string }

func (d fakeDeployment) TelemetryIdentifier() string      { return d.uid }
func (d fakeDeployment) AutomationDeploymentName() string { return d.name }

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
}

func runMultiReconcileAndRequireMonitoringMapping(ctx context.Context, t *testing.T, reconciler reconcile.Reconciler, commonController *ReconcileCommonController, kubeClient kubernetesClient.Client, object *mdbmulti.MongoDBMultiCluster, deploymentName string) {
	t.Helper()
	recorder := installMonitoringMappingRecorder(commonController)

	checkMultiReconcileSuccessful(ctx, t, reconciler, object, kubeClient, false)

	requireMonitoringMapping(t, recorder, deploymentName, string(object.UID))
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

			common.EnsureOtelExporterForDeployment(conn, "agent-key", fakeDeployment{uid: "uid-1", name: "rs"}, zap.S())

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
		common.EnsureOtelExporterForDeployment(conn, "agent-key", fakeDeployment{uid: "uid-1", name: "rs"}, zap.S())
		common.ReleaseOtelExporterForProject(ctx, DefaultReplicaSetBuilder().Build(), "g1", zap.S())
		common.ReleaseOtelExporterForConnection(conn, "", zap.S())
	})
}

func TestReleaseOtelExporterForProject_UsesStatusAndConfigMap(t *testing.T) {
	ctx := context.Background()
	kubeClient, _ := mock.NewDefaultFakeClient()
	common := NewReconcileCommonController(ctx, kubeClient, nil)
	recorder := installMonitoringMappingRecorder(common)

	common.ReleaseOtelExporterForProject(ctx, DefaultReplicaSetBuilder().Build(), "g1", zap.S())
	common.ReleaseOtelExporterForProject(ctx, DefaultReplicaSetBuilder().Build(), "", zap.S())

	require.Equal(t, []ommetrics.Project{{BaseURL: "http://mycompany.example.com:8080", GroupID: "g1"}}, recorder.released)
}

func TestReleaseOtelExporterForConnection_OnlyWithoutStatusProjectID(t *testing.T) {
	common := &ReconcileCommonController{}
	recorder := installMonitoringMappingRecorder(common)
	conn := om.NewEmptyMockedOmConnection(&om.OMContext{BaseURL: "https://om.example.com", GroupID: "g1"})

	common.ReleaseOtelExporterForConnection(conn, "g1", zap.S())
	require.Empty(t, recorder.released)

	common.ReleaseOtelExporterForConnection(conn, "", zap.S())
	require.Equal(t, []ommetrics.Project{{BaseURL: "https://om.example.com", GroupID: "g1"}}, recorder.released)
}
