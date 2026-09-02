package operator

import (
	"context"
	"maps"
	"reflect"

	"go.uber.org/zap"
	"golang.org/x/xerrors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"

	coordinationv1 "k8s.io/api/coordination/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	operatorv1 "github.com/mongodb/mongodb-kubernetes/api/operator/v1"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster"
)

// directiveTransport is the delivery seam: every cross-cluster interaction in this design is
// "place a small fact in cluster X" / "read the facts back" — who executes the local write is a
// free variable (API server today, an RPC between operators would fit the same seam). It covers the only two
// cross-cluster interaction families, directive delivery and vote acquisition (the majority
// lease's read/CAS). The API-server transport is the POC's only implementation; an RPC variant
// stays selectable by field data.
type directiveTransport interface {
	// WriteDirective places the planned directive spec for a deployment on one member cluster.
	WriteDirective(ctx context.Context, clusterName string, nsName types.NamespacedName, desired operatorv1.MongoDBDirectiveSpec) error
	// ReadDirectives reads this deployment's directive on every known cluster. NotFound and a
	// failed read are different worlds: NotFound is an authoritative "no entry here", a failed
	// read is absence of visibility — the allocation guard must not mint new indexes over the
	// latter.
	ReadDirectives(ctx context.Context, nsName types.NamespacedName, log *zap.SugaredLogger) map[string]directiveView
	// ReadLease reads one cluster's election lease. The error passes through untranslated: the
	// elector distinguishes NotFound (an authoritative absence, an observation) from any other
	// failure (absence of visibility, never an observation).
	ReadLease(ctx context.Context, clusterName string, nsName types.NamespacedName) (*coordinationv1.Lease, error)
	// WriteLease executes one CAS intent: create when the lease carries no resourceVersion,
	// otherwise an update conditioned on the carried resourceVersion.
	WriteLease(ctx context.Context, clusterName string, lease *coordinationv1.Lease) error
}

// apiServerTransport delivers directives and lease CAS writes directly into each member
// cluster's API server through per-cluster clients. Lease reads go through the uncached readers
// when provided: a CAS over a cached read is wrong by construction (writes hit the API server
// either way).
type apiServerTransport struct {
	clients map[string]kubernetesClient.Client
	readers map[string]client.Reader
}

var _ directiveTransport = &apiServerTransport{}

func newAPIServerTransport(memberClustersMap map[string]client.Client) *apiServerTransport {
	clientsMap := make(map[string]kubernetesClient.Client)
	for k, v := range memberClustersMap {
		clientsMap[k] = kubernetesClient.NewClient(v)
	}
	return &apiServerTransport{clients: clientsMap}
}

// newAPIServerTransportFromClusters is the production constructor: cache-backed clients for
// directive delivery, the clusters' API readers for the vote family's uncached reads.
func newAPIServerTransportFromClusters(memberClustersMap map[string]cluster.Cluster) *apiServerTransport {
	transport := newAPIServerTransport(multicluster.ClustersMapToClientMap(memberClustersMap))
	transport.readers = make(map[string]client.Reader)
	for k, v := range memberClustersMap {
		transport.readers[k] = v.GetAPIReader()
	}
	return transport
}

// WriteDirective is a read-modify-write: the stored allocation map is unioned into the desired
// one (a stored entry the planner did not carry is preserved — a stale leader's single write must
// never regress a copy), AdvancedAt is persisted only when the instruction actually changed, and
// an unchanged spec skips the write entirely. A resourceVersion conflict (the member bumps it
// with status writes) is a transient error; controller-runtime retries. The directive carries no
// owner reference: it is leader-managed and usually lives on a foreign cluster, where GC would
// never fire. These merge semantics are transport-impl behavior, not caller policy: an RPC
// transport's receiving side would own the same read-modify-write against its local store.
func (t *apiServerTransport) WriteDirective(ctx context.Context, clusterName string, nsName types.NamespacedName, desired operatorv1.MongoDBDirectiveSpec) error {
	memberClient, ok := t.clients[clusterName]
	if !ok {
		return xerrors.Errorf("no client for member cluster %s", clusterName)
	}

	directive := operatorv1.MongoDBDirective{}
	err := memberClient.Get(ctx, nsName, &directive)
	if apiErrors.IsNotFound(err) {
		directive = operatorv1.MongoDBDirective{
			ObjectMeta: metav1.ObjectMeta{Name: nsName.Name, Namespace: nsName.Namespace},
			Spec:       desired,
		}
		return memberClient.Create(ctx, &directive)
	}
	if err != nil {
		return err
	}

	merged := maps.Clone(desired.IndexAllocations)
	for cluster, index := range directive.Spec.IndexAllocations {
		if _, ok := merged[cluster]; !ok {
			merged[cluster] = index
		}
	}
	desired.IndexAllocations = merged

	unchanged := desired
	unchanged.AdvancedAt = directive.Spec.AdvancedAt
	if reflect.DeepEqual(directive.Spec, unchanged) {
		return nil // write-quiescence; keeping the old AdvancedAt also keeps stuckness visible
	}

	directive.Spec = desired
	return memberClient.Update(ctx, &directive)
}

// ReadLease reads one cluster's election lease, bypassing any cache: the read feeds a CAS, and
// a CAS on a stale read is wrong by construction.
func (t *apiServerTransport) ReadLease(ctx context.Context, clusterName string, nsName types.NamespacedName) (*coordinationv1.Lease, error) {
	reader, ok := t.readers[clusterName]
	if !ok {
		memberClient, clientOK := t.clients[clusterName]
		if !clientOK {
			return nil, xerrors.Errorf("no client for member cluster %s", clusterName)
		}
		reader = memberClient
	}
	lease := &coordinationv1.Lease{}
	if err := reader.Get(ctx, nsName, lease); err != nil {
		return nil, err
	}
	return lease, nil
}

// WriteLease executes one CAS intent. The API server's optimistic concurrency IS the protocol's
// CAS: a create fails on an existing object, an update fails on a moved resourceVersion — both
// come back untranslated for the elector to count as a lost race.
func (t *apiServerTransport) WriteLease(ctx context.Context, clusterName string, lease *coordinationv1.Lease) error {
	memberClient, ok := t.clients[clusterName]
	if !ok {
		return xerrors.Errorf("no client for member cluster %s", clusterName)
	}
	if lease.ResourceVersion == "" {
		return memberClient.Create(ctx, lease)
	}
	return memberClient.Update(ctx, lease)
}

func (t *apiServerTransport) ReadDirectives(ctx context.Context, nsName types.NamespacedName, log *zap.SugaredLogger) map[string]directiveView {
	views := make(map[string]directiveView, len(t.clients))
	for clusterName, memberClient := range t.clients {
		directive := operatorv1.MongoDBDirective{}
		if err := memberClient.Get(ctx, nsName, &directive); err != nil {
			if apiErrors.IsNotFound(err) {
				views[clusterName] = directiveView{Exists: false}
				continue
			}
			log.Warnf("Failed reading the directive on cluster %s: %s", clusterName, err)
			views[clusterName] = directiveView{Unreachable: true}
			continue
		}
		views[clusterName] = directiveView{
			Exists:     true,
			Spec:       directive.Spec,
			Status:     directive.Status,
			Generation: directive.Generation,
		}
	}
	return views
}
