package operator

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	"github.com/mongodb/mongodb-kubernetes/pkg/dns"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/architectures"
)

// acReadCounter counts the automation config reads that reach a connection.
// HTTPOmConnection coalesces these reads into one GET, so this count is the
// upper bound on the automation config requests a reconcile can issue.
type acReadCounter struct {
	om.Connection
	reads []string
}

func (c *acReadCounter) count(operation string) {
	c.reads = append(c.reads, operation)
}

func (c *acReadCounter) reset() {
	c.reads = nil
}

func (c *acReadCounter) ReadDeployment(ctx context.Context) (om.Deployment, error) {
	c.count("ReadDeployment")
	return c.Connection.ReadDeployment(ctx)
}

func (c *acReadCounter) ReadUpdateDeployment(ctx context.Context, depFunc func(om.Deployment) error, log *zap.SugaredLogger) error {
	c.count("ReadUpdateDeployment")
	return c.Connection.ReadUpdateDeployment(ctx, depFunc, log)
}

func (c *acReadCounter) ReadAutomationConfig(ctx context.Context) (*om.AutomationConfig, error) {
	c.count("ReadAutomationConfig")
	return c.Connection.ReadAutomationConfig(ctx)
}

func (c *acReadCounter) ReadUpdateAutomationConfig(ctx context.Context, modifyACFunc func(*om.AutomationConfig) error, log *zap.SugaredLogger) error {
	c.count("ReadUpdateAutomationConfig")
	return c.Connection.ReadUpdateAutomationConfig(ctx, modifyACFunc, log)
}

func (c *acReadCounter) GetAgentAuthMode(ctx context.Context) (string, error) {
	c.count("GetAgentAuthMode")
	return c.Connection.GetAgentAuthMode(ctx)
}

// TestAutomationConfigReadsPerReplicaSetReconcile pins the automation config
// reads of a replica set reconcile that reaches the Ops Manager update phase.
// The reads share one response through HTTPOmConnection, so each read beyond the
// first costs a decode but not a request.
func TestAutomationConfigReadsPerReplicaSetReconcile(t *testing.T) {
	ctx := context.Background()
	rs := DefaultReplicaSetBuilder().Build()

	inner := om.NewEmptyMockedOmConnection(&om.OMContext{}).(*om.MockedOmConnection)
	inner.Hostnames = calculateReplicaSetExternalHostnames(rs)
	conn := &acReadCounter{Connection: inner}
	factory := om.NewCachedOMConnectionFactory(func(*om.OMContext) om.Connection { return conn })
	kubeClient := mock.NewDefaultFakeClientWithOMConnectionFactory(factory, rs)
	require.NoError(t, kubeClient.Update(ctx, rs))

	reconciler := newReplicaSetReconciler(ctx, kubeClient, nil, "", "", false, false, false, "", architectures.NonStatic, factory.GetConnectionFunc)

	_, err := reconciler.Reconcile(ctx, requestFromObject(rs))
	require.NoError(t, err)

	// The first reconcile creates the StatefulSet. Register its hosts as agents in
	// the mocked connection so that the next reconcile passes the agent
	// registration wait and reaches the automation config read path.
	sts, err := kubeClient.GetStatefulSet(ctx, mock.ObjectKeyFromApiObject(rs))
	require.NoError(t, err)
	hostnames, _ := dns.GetDnsForStatefulSet(sts, "", nil)
	inner.AddHosts(hostnames)

	conn.reset()
	_, err = reconciler.Reconcile(ctx, requestFromObject(rs))
	require.NoError(t, err)

	require.Equal(t, []string{
		"ReadDeployment",             // checkIfHasExcessProcesses
		"ReadAutomationConfig",       // validateMongoDBResource
		"GetAgentAuthMode",           // Reconcile
		"ReadDeployment",             // ensureRoles
		"ReadDeployment",             // updateOmDeploymentRs
		"ReadAutomationConfig",       // updateOmAuthentication
		"ReadAutomationConfig",       // authentication.Disable
		"ReadUpdateAutomationConfig", // authentication.Disable
		"ReadUpdateDeployment",       // updateOmDeploymentRs
	}, conn.reads)
}
