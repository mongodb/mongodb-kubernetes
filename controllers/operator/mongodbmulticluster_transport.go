package operator

import (
	"context"
	"maps"
	"reflect"

	"go.uber.org/zap"
	"golang.org/x/xerrors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	operatorv1 "github.com/mongodb/mongodb-kubernetes/api/operator/v1"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
)

// directiveTransport is the delivery seam: every cross-cluster interaction in this design is
// "place a small fact in cluster X" / "read the facts back" — who executes the local write is a
// free variable (.spike/design/transport-seam-tcp-vs-apiserver.md). The API-server transport is
// the POC's only implementation; an RPC variant stays selectable by field data. M3.7 grows this
// interface with the vote family (lease read/CAS).
type directiveTransport interface {
	// WriteDirective places the planned directive spec for a deployment on one member cluster.
	WriteDirective(ctx context.Context, clusterName string, nsName types.NamespacedName, desired operatorv1.MongoDBDirectiveSpec) error
	// ReadDirectives reads this deployment's directive on every known cluster. NotFound and a
	// failed read are different worlds: NotFound is an authoritative "no entry here", a failed
	// read is absence of visibility — the allocation guard must not mint new indexes over the
	// latter.
	ReadDirectives(ctx context.Context, nsName types.NamespacedName, log *zap.SugaredLogger) map[string]directiveView
}

// apiServerTransport delivers directives by writing them directly into each member cluster's
// API server through per-cluster clients.
type apiServerTransport struct {
	clients map[string]kubernetesClient.Client
}

var _ directiveTransport = &apiServerTransport{}

func newAPIServerTransport(memberClustersMap map[string]client.Client) *apiServerTransport {
	clientsMap := make(map[string]kubernetesClient.Client)
	for k, v := range memberClustersMap {
		clientsMap[k] = kubernetesClient.NewClient(v)
	}
	return &apiServerTransport{clients: clientsMap}
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
