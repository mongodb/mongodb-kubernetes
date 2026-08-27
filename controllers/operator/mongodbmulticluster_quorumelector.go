package operator

import (
	"context"
	"sync"
	"time"

	"go.uber.org/zap"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/event"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	toolscache "k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/leaderelection"

	mdbmultiv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdbmulti"
)

// defaultLeaseDuration is the protocol's ONE time constant. Everything else derives from it:
// renewal cadence and the randomized candidacy delay are duration/3, the renew deadline is
// 2×duration/3, expiry, restart blindness and the takeover hold-off are one full duration.
// It is also carried in every written Lease object, so it is changeable without a redeploy.
const defaultLeaseDuration = 30 * time.Second

// QuorumElector is the majority-lease Elector: a manager.Runnable owning one independent
// election per MongoDBMultiCluster deployment (the lease ensemble follows each spec's cluster
// list, so an operator-wide election has no coherent electorate). Entries are created and
// retired from the local cache's informer on the CR; each entry drives a quorumLock with
// client-go's stock LeaderElector, wrapped in a run-forever loop because losing leadership here
// is a NORMAL transition — the same process keeps running the member controller and must keep
// contending. Heartbeats run on this runnable's own goroutines and never queue behind a slow
// reconcile.
//
// Sanity checks recorded per the protocol doc:
//
// (a) Longest guarded action vs renewal cadence: the OM client (om/api/http.go) is a
// retryablehttp client with NO per-request timeout and up to 3 retries waiting 1–10s each — a
// single AC PUT can exceed both the renewal cadence (duration/3 = 10s) and the renew deadline
// (2×duration/3 = 20s). That is exactly why the elector is a separate runnable: a slow PUT can
// never depose a healthy leader by starving its heartbeat. The residual risk — a still-in-flight
// zombie PUT landing after a takeover — is what the hold-off is sized against, and an
// arbitrarily slow PUT can in principle outlive it; OM-side locking (CLOUDP-373090) is the real
// fence there, noted in the leader controller's AC notes.
//
// (b) "Elector loses its majority mid-reconcile while the snapshot says leader at term 8": the
// reconcile in flight fires at most ONE guarded action (one-action-per-reconcile bounds the
// exposure; the next pass re-reads Current). A directive write at term 8 is absorbed by the
// member's term fence and rewritten by the term-9 successor — one write of churn, no
// oscillation (the T17 world test pins it). An AC write has no server-side CAS; the successor's
// takeover hold-off (one full duration before ITS first guarded write) is the window sized for
// the zombie's write to land first, and the AC's term marker plus the T16 floor keep terms
// monotonic. Absorbing rule, by name: term fences + takeover hold-off.
type QuorumElector struct {
	localCache    cache.Cache
	transport     directiveTransport
	self          string
	leaseDuration time.Duration
	events        chan event.GenericEvent
	log           *zap.SugaredLogger

	mu      sync.Mutex
	entries map[types.NamespacedName]*electorEntry
}

type electorEntry struct {
	lock   *quorumLock
	cancel context.CancelFunc
	done   chan struct{}
}

var _ Elector = &QuorumElector{}

// NewQuorumElector wires the elector over the same cluster map the leader controller plans
// against; the manager itself is in the map as the local cluster.
func NewQuorumElector(localCache cache.Cache, memberClustersMap map[string]cluster.Cluster, self string) *QuorumElector {
	return newQuorumElector(localCache, newAPIServerTransportFromClusters(memberClustersMap), self, defaultLeaseDuration)
}

func newQuorumElector(localCache cache.Cache, transport directiveTransport, self string, leaseDuration time.Duration) *QuorumElector {
	return &QuorumElector{
		localCache:    localCache,
		transport:     transport,
		self:          self,
		leaseDuration: leaseDuration,
		// transitions are wake-up hints for a level-triggered reconciler: buffered so elector
		// goroutines never block on a busy controller, droppable for the same reason
		events:  make(chan event.GenericEvent, 256),
		log:     zap.S().With("QuorumElector", self),
		entries: map[types.NamespacedName]*electorEntry{},
	}
}

// Start runs until the manager stops: entries follow the CR through the local informer, and on
// shutdown every entry is cancelled — ReleaseOnCancel then writes the clean-release shape, so an
// orderly operator restart hands leadership over without the successor's hold-off.
func (e *QuorumElector) Start(ctx context.Context) error {
	informer, err := e.localCache.GetInformer(ctx, &mdbmultiv1.MongoDBMultiCluster{})
	if err != nil {
		return err
	}
	registration, err := informer.AddEventHandler(toolscache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { e.handleUpsert(ctx, obj) },
		UpdateFunc: func(_, newObj interface{}) { e.handleUpsert(ctx, newObj) },
		DeleteFunc: e.handleDelete,
	})
	if err != nil {
		return err
	}
	e.log.Infof("Quorum elector started (lease duration %s)", e.leaseDuration)

	<-ctx.Done()
	if err := informer.RemoveEventHandler(registration); err != nil {
		e.log.Debugf("Failed removing the informer handler: %s", err)
	}
	e.retireAll()
	return nil
}

func (e *QuorumElector) handleUpsert(ctx context.Context, obj interface{}) {
	m, ok := obj.(*mdbmultiv1.MongoDBMultiCluster)
	if !ok {
		return
	}
	electorate := make([]string, 0, len(m.Spec.ClusterSpecList))
	for _, item := range m.Spec.ClusterSpecList {
		electorate = append(electorate, item.ClusterName)
	}
	e.upsertDeployment(ctx, types.NamespacedName{Namespace: m.Namespace, Name: m.Name}, electorate)
}

func (e *QuorumElector) handleDelete(obj interface{}) {
	if tombstone, ok := obj.(toolscache.DeletedFinalStateUnknown); ok {
		obj = tombstone.Obj
	}
	m, ok := obj.(*mdbmultiv1.MongoDBMultiCluster)
	if !ok {
		return
	}
	e.removeDeployment(types.NamespacedName{Namespace: m.Namespace, Name: m.Name})
}

// upsertDeployment creates the deployment's election on first sight and follows electorate
// changes afterwards (the majority is always counted against the CURRENT spec's cluster list).
func (e *QuorumElector) upsertDeployment(ctx context.Context, nsName types.NamespacedName, electorate []string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if entry, ok := e.entries[nsName]; ok {
		entry.lock.SetElectorate(electorate)
		return
	}
	entryCtx, cancel := context.WithCancel(ctx)
	entry := &electorEntry{
		lock:   newQuorumLock(nsName, e.self, electorate, e.leaseDuration, e.transport, e.log),
		cancel: cancel,
		done:   make(chan struct{}),
	}
	e.entries[nsName] = entry
	go e.contend(entryCtx, nsName, entry)
}

func (e *QuorumElector) removeDeployment(nsName types.NamespacedName) {
	e.mu.Lock()
	entry, ok := e.entries[nsName]
	if ok {
		delete(e.entries, nsName)
	}
	e.mu.Unlock()
	if ok {
		entry.cancel()
		<-entry.done
	}
}

func (e *QuorumElector) retireAll() {
	e.mu.Lock()
	entries := e.entries
	e.entries = map[types.NamespacedName]*electorEntry{}
	e.mu.Unlock()
	for _, entry := range entries {
		entry.cancel()
		<-entry.done
	}
}

// contend is the run-forever loop around client-go's LeaderElector, whose Run returns on
// leadership loss (standby-pod semantics we cannot use — the member controller keeps running,
// and this operator must keep contending). ReleaseOnCancel gives the correct step-down ordering
// for free: the guarded-work context is cancelled first, the release write comes after.
func (e *QuorumElector) contend(ctx context.Context, nsName types.NamespacedName, entry *electorEntry) {
	defer close(entry.done)
	for ctx.Err() == nil {
		leaderElector, err := leaderelection.NewLeaderElector(leaderelection.LeaderElectionConfig{
			Lock:            entry.lock,
			LeaseDuration:   e.leaseDuration,
			RenewDeadline:   e.leaseDuration * 2 / 3,
			RetryPeriod:     e.leaseDuration / 3,
			ReleaseOnCancel: true,
			Name:            nsName.String(),
			Callbacks: leaderelection.LeaderCallbacks{
				OnStartedLeading: func(leadCtx context.Context) {
					e.notify(nsName)
					// a dirty acquire leaves Current() answering non-leader until the hold-off
					// is served, and that flip is silent — schedule the wake-up that lets the
					// new leader actually start planning
					if remaining := entry.lock.HoldOffRemaining(); remaining > 0 {
						go func() {
							select {
							case <-leadCtx.Done():
							case <-time.After(remaining):
								e.notify(nsName)
							}
						}()
					}
				},
				OnStoppedLeading: func() { e.notify(nsName) },
				OnNewLeader:      func(string) { e.notify(nsName) },
			},
		})
		if err != nil {
			e.log.Errorf("Failed building the leader elector for %s: %s", nsName, err)
		} else {
			leaderElector.Run(ctx)
		}
		select {
		case <-ctx.Done():
		case <-time.After(e.leaseDuration / 3):
		}
	}
}

// notify pushes a wake-up for the deployment into the leader controller's source.Channel watch.
// Dropping under backpressure is fine: reconciles are level-triggered and re-read Current().
func (e *QuorumElector) notify(nsName types.NamespacedName) {
	wakeUp := event.GenericEvent{Object: &mdbmultiv1.MongoDBMultiCluster{
		ObjectMeta: metav1.ObjectMeta{Name: nsName.Name, Namespace: nsName.Namespace},
	}}
	select {
	case e.events <- wakeUp:
	default:
		e.log.Debugf("Dropped a leadership wake-up for %s (channel full)", nsName)
	}
}

// Current pulls the deployment's leadership belief; an unknown deployment (no CR seen yet, or
// just retired) is never leader.
func (e *QuorumElector) Current(deployment types.NamespacedName) (term int64, isLeader bool) {
	e.mu.Lock()
	entry, ok := e.entries[deployment]
	e.mu.Unlock()
	if !ok {
		return 0, false
	}
	return entry.lock.Current()
}

func (e *QuorumElector) Events() <-chan event.GenericEvent {
	return e.events
}

func (e *QuorumElector) ObserveTermFloor(deployment types.NamespacedName, floor int64) {
	e.mu.Lock()
	entry, ok := e.entries[deployment]
	e.mu.Unlock()
	if ok {
		entry.lock.ObserveTermFloor(floor)
	}
}
