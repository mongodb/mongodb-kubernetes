package operator

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"

	coordinationv1 "k8s.io/api/coordination/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
)

func TestWriteLeaseCASSemantics(t *testing.T) {
	ctx := context.Background()
	transport := &apiServerTransport{clients: map[string]kubernetesClient.Client{
		clusters[0]: kubernetesClient.NewClient(mock.NewEmptyFakeClientBuilder().Build()),
	}}
	nsName := kube.ObjectKey("testns", "temple")

	_, err := transport.ReadLease(ctx, clusters[0], nsName)
	assert.True(t, apiErrors.IsNotFound(err), "an absent lease reads as NotFound, untranslated")

	fresh := &coordinationv1.Lease{ObjectMeta: metav1.ObjectMeta{Name: nsName.Name, Namespace: nsName.Namespace}}
	setLeaseTerm(fresh, 3)
	require.NoError(t, transport.WriteLease(ctx, clusters[0], fresh), "no resourceVersion means create")

	duplicate := &coordinationv1.Lease{ObjectMeta: metav1.ObjectMeta{Name: nsName.Name, Namespace: nsName.Namespace}}
	assert.True(t, apiErrors.IsAlreadyExists(transport.WriteLease(ctx, clusters[0], duplicate)), "a lost create race comes back as AlreadyExists")

	read, err := transport.ReadLease(ctx, clusters[0], nsName)
	require.NoError(t, err)
	term, ok := leaseTerm(read)
	assert.True(t, ok)
	assert.Equal(t, int64(3), term)

	setLeaseTerm(read, 4)
	require.NoError(t, transport.WriteLease(ctx, clusters[0], read), "an update at the read resourceVersion lands")

	stale := read.DeepCopy()
	stale.ResourceVersion = "1" // the write above moved it
	setLeaseTerm(stale, 5)
	assert.True(t, apiErrors.IsConflict(transport.WriteLease(ctx, clusters[0], stale)), "a moved resourceVersion comes back as Conflict — the lost CAS race")
}

func TestReadLeaseUsesTheUncachedReader(t *testing.T) {
	ctx := context.Background()
	nsName := kube.ObjectKey("testns", "temple")
	lease := &coordinationv1.Lease{ObjectMeta: metav1.ObjectMeta{Name: nsName.Name, Namespace: nsName.Namespace}}

	// the lease exists only on the reader side: a read served by the (cache-backed in
	// production) client would come back NotFound
	transport := &apiServerTransport{
		clients: map[string]kubernetesClient.Client{
			clusters[0]: kubernetesClient.NewClient(mock.NewEmptyFakeClientBuilder().Build()),
		},
		readers: map[string]client.Reader{
			clusters[0]: mock.NewEmptyFakeClientBuilder().WithObjects(lease).Build(),
		},
	}

	read, err := transport.ReadLease(ctx, clusters[0], nsName)
	require.NoError(t, err, "the vote family reads through GetAPIReader, never the cache")
	assert.Equal(t, nsName.Name, read.Name)
}

func TestLeaseTransportUnknownCluster(t *testing.T) {
	ctx := context.Background()
	transport := &apiServerTransport{clients: map[string]kubernetesClient.Client{}}

	_, err := transport.ReadLease(ctx, "nowhere", kube.ObjectKey("testns", "temple"))
	assert.ErrorContains(t, err, "no client for member cluster nowhere")
	err = transport.WriteLease(ctx, "nowhere", &coordinationv1.Lease{})
	assert.ErrorContains(t, err, "no client for member cluster nowhere")
}
