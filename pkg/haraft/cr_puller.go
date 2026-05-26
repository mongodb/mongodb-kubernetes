package haraft

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mdbmultiv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdbmulti"
)

// ReplicaSourceAnnotation is set on every locally mirrored MongoDBMultiCluster
// replica. The value is the cluster name from which the replica was pulled.
const ReplicaSourceAnnotation = "haraft.mongodb.com/replica-source"

// defaultResyncInterval is the re-sync cadence for the CR Puller. It currently
// uses periodic List polling rather than a Kubernetes Watch stream: this keeps
// the implementation small and is well within the seconds-scale latency budget
// of the multi-cluster reconciler.
const defaultResyncInterval = 2 * time.Second

// CRPuller mirrors MongoDBMultiCluster CRs from a leader cluster into the
// local cluster's API. It is created once at process start and reconfigured
// via Restart whenever leadership changes. The puller is gated: when the
// local node becomes leader, the watch is stopped (the leader's local CRs
// are canonical, not pulled).
type CRPuller struct {
	localClusterName string
	namespace        string
	localClient      client.Client
	peerClients      map[string]client.Client
	raftNode         *RaftNode

	mu            sync.Mutex
	currentLeader string
	cancelWatch   context.CancelFunc
	wg            sync.WaitGroup

	// _runCount is incremented atomically each time run() starts; used by
	// tests to verify idempotency without relying on function pointer identity.
	_runCount int32

	// Tunables; default values applied by NewCRPuller.
	backoffMin time.Duration
	backoffMax time.Duration
}

// NewCRPuller constructs a puller. It does NOT start anything — callers must
// invoke Start (or Restart) after construction.
func NewCRPuller(localClusterName, namespace string, localClient client.Client, peerClients map[string]client.Client, raftNode *RaftNode) *CRPuller {
	return &CRPuller{
		localClusterName: localClusterName,
		namespace:        namespace,
		localClient:      localClient,
		peerClients:      peerClients,
		raftNode:         raftNode,
		backoffMin:       1 * time.Second,
		backoffMax:       30 * time.Second,
	}
}

// runCount returns the number of times run() has been started. Used by tests.
func (p *CRPuller) runCount() int32 {
	return atomic.LoadInt32(&p._runCount)
}

// upsertLocalReplica writes (or refreshes) the local replica of cr. The
// ReplicaSourceAnnotation records which leader cluster the data came from
// so syncOnce (added in a later task) can identify orphans to delete.
func (p *CRPuller) upsertLocalReplica(ctx context.Context, source string, cr *mdbmultiv1.MongoDBMultiCluster) error {
	key := types.NamespacedName{Name: cr.Name, Namespace: cr.Namespace}
	existing := &mdbmultiv1.MongoDBMultiCluster{}
	err := p.localClient.Get(ctx, key, existing)
	switch {
	case apiErrors.IsNotFound(err):
		fresh := &mdbmultiv1.MongoDBMultiCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:        cr.Name,
				Namespace:   cr.Namespace,
				Labels:      cr.Labels,
				Annotations: mergeReplicaAnnotation(cr.Annotations, source),
			},
			Spec: cr.Spec,
		}
		return p.localClient.Create(ctx, fresh)
	case err != nil:
		return err
	default:
		existing.Spec = cr.Spec
		existing.Labels = cr.Labels
		existing.Annotations = mergeReplicaAnnotation(cr.Annotations, source)
		return p.localClient.Update(ctx, existing)
	}
}

// deleteLocalReplica is idempotent; a missing replica is not an error.
func (p *CRPuller) deleteLocalReplica(ctx context.Context, key types.NamespacedName) error {
	existing := &mdbmultiv1.MongoDBMultiCluster{}
	if err := p.localClient.Get(ctx, key, existing); err != nil {
		if apiErrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	return p.localClient.Delete(ctx, existing)
}

// syncOnce reconciles the local replicas with the current set of CRs on the
// leader cluster: it upserts every leader-side CR and deletes any local
// replica that no longer exists on the leader (identified by the
// ReplicaSourceAnnotation pointing at sourceCluster).
func (p *CRPuller) syncOnce(ctx context.Context, sourceCluster string) error {
	leaderCl, ok := p.peerClients[sourceCluster]
	if !ok {
		return fmt.Errorf("CRPuller: no client for source cluster %q", sourceCluster)
	}

	leaderList := &mdbmultiv1.MongoDBMultiClusterList{}
	if err := leaderCl.List(ctx, leaderList, client.InNamespace(p.namespace)); err != nil {
		return err
	}
	leaderNames := map[string]struct{}{}
	for i := range leaderList.Items {
		cr := &leaderList.Items[i]
		leaderNames[cr.Name] = struct{}{}
		if err := p.upsertLocalReplica(ctx, sourceCluster, cr); err != nil {
			return err
		}
	}

	localList := &mdbmultiv1.MongoDBMultiClusterList{}
	if err := p.localClient.List(ctx, localList, client.InNamespace(p.namespace)); err != nil {
		return err
	}
	for i := range localList.Items {
		local := &localList.Items[i]
		if local.Annotations[ReplicaSourceAnnotation] != sourceCluster {
			continue // not a replica from this source — leave alone
		}
		if _, present := leaderNames[local.Name]; present {
			continue
		}
		if err := p.deleteLocalReplica(ctx, types.NamespacedName{Name: local.Name, Namespace: local.Namespace}); err != nil {
			return err
		}
	}
	return nil
}

func mergeReplicaAnnotation(src map[string]string, sourceCluster string) map[string]string {
	out := make(map[string]string, len(src)+1)
	for k, v := range src {
		out[k] = v
	}
	out[ReplicaSourceAnnotation] = sourceCluster
	return out
}

// Start begins mirroring CRs from leaderClusterName. Safe to call multiple
// times — subsequent calls with the same leader are no-ops; calls with a
// different leader behave like Restart.
func (p *CRPuller) Start(ctx context.Context, leaderClusterName string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.raftNode != nil && p.raftNode.IsLeader() {
		// We're the leader — our local CRs are canonical, nothing to pull.
		return
	}
	if leaderClusterName == "" || leaderClusterName == p.localClusterName {
		return
	}
	if p.currentLeader == leaderClusterName && p.cancelWatch != nil {
		return
	}
	if p.cancelWatch != nil {
		p.cancelWatch()
		p.cancelWatch = nil
	}
	runCtx, cancel := context.WithCancel(ctx)
	p.currentLeader = leaderClusterName
	p.cancelWatch = cancel
	p.wg.Add(1)
	go p.run(runCtx, leaderClusterName)
}

// Stop tears down the current watch, if any, and waits for the runner goroutine
// to exit. Idempotent.
func (p *CRPuller) Stop() {
	p.mu.Lock()
	if p.cancelWatch != nil {
		p.cancelWatch()
		p.cancelWatch = nil
	}
	p.currentLeader = ""
	p.mu.Unlock()

	// Drain the runner outside the lock so run() can exit without deadlocking.
	p.wg.Wait()
}

// Restart atomically replaces the current watch with one targeting a new
// leader. The prior goroutine is fully drained (via Stop's WaitGroup) before
// the new one is spawned, preventing concurrent syncOnce calls.
func (p *CRPuller) Restart(ctx context.Context, leaderClusterName string) {
	p.Stop()
	p.Start(ctx, leaderClusterName)
}

// OnLeadershipChange is registered with RaftNode.OnLeadershipChange. When this
// node wins leadership the puller stops; when it loses leadership and a leader
// is known, the puller starts against that leader.
func (p *CRPuller) OnLeadershipChange(ctx context.Context, isLeader bool) {
	if isLeader {
		p.Stop()
		return
	}
	if p.raftNode == nil {
		return
	}
	leader := p.raftNode.Leader()
	if leader == "" {
		return
	}
	p.Start(ctx, leader)
}

func (p *CRPuller) run(ctx context.Context, leaderClusterName string) {
	atomic.AddInt32(&p._runCount, 1)
	defer p.wg.Done()

	backoff := p.backoffMin
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if err := p.syncOnce(ctx, leaderClusterName); err != nil {
			zap.S().Warnf("haraft: CRPuller sync error from %s: %v", leaderClusterName, err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff *= 2
			if backoff > p.backoffMax {
				backoff = p.backoffMax
			}
			continue
		}
		backoff = p.backoffMin
		select {
		case <-ctx.Done():
			return
		case <-time.After(defaultResyncInterval):
		}
	}
}
