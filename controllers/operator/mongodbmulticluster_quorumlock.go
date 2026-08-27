package operator

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"go.uber.org/zap"
	"golang.org/x/xerrors"

	coordinationv1 "k8s.io/api/coordination/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
)

// quorumLock is the majority lease as a client-go resourcelock.Interface backend: client-go's
// stock LeaderElector supplies the hardened timing loop (retry cadence, renew deadline,
// release-on-cancel ordering), and this backend supplies the only novel parts — the N-lease
// aggregation and the CAS fan-out, both through the transport seam.
//
// Leadership is a count projected onto client-go's string compare: Get aggregates the N leases
// into ONE composite record whose HolderIdentity is the majority holder (or "" when there is
// none — a minority holder never makes anyone defer), whose raw bytes change iff some lease's
// content changed (client-go's observedTime expiry over the bytes is then exactly the quorum's
// frozen-content verdict), and whose expiry window is the one leaseDuration. Update executes the
// core's CAS intents and succeeds only when a majority landed in THIS batch.
type quorumLock struct {
	deployment types.NamespacedName
	transport  directiveTransport
	log        *zap.SugaredLogger
	clock      func() time.Time

	mu   sync.Mutex
	core *quorumLeaseCore
}

var _ resourcelock.Interface = &quorumLock{}

func newQuorumLock(deployment types.NamespacedName, self string, electorate []string, leaseDuration time.Duration, transport directiveTransport, log *zap.SugaredLogger) *quorumLock {
	core := newQuorumLeaseCore(self, leaseDuration)
	core.setElectorate(electorate)
	return &quorumLock{
		deployment: deployment,
		transport:  transport,
		log:        log,
		clock:      time.Now,
		core:       core,
	}
}

// Get reads every configured cluster's lease (uncached, through the seam) and aggregates them
// into the composite record. It never returns NotFound: an all-absent world is an empty
// composite (holder ""), which client-go treats as immediately acquirable — the CAS fan-out in
// Update is where races are decided, not Create.
func (q *quorumLock) Get(ctx context.Context) (*resourcelock.LeaderElectionRecord, []byte, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	now := q.clock()
	for _, cluster := range q.core.electorate {
		q.readThrough(ctx, cluster, now)
	}
	return q.compositeRecord(), q.core.contentFingerprint(), nil
}

// readThrough reads one cluster's lease and feeds the core: NotFound is the authoritative
// absence observation; any other failure is absence of visibility and observes nothing.
func (q *quorumLock) readThrough(ctx context.Context, cluster string, now time.Time) {
	lease, err := q.transport.ReadLease(ctx, cluster, q.deployment)
	if apiErrors.IsNotFound(err) {
		q.core.observe(cluster, leaseContent{}, "", now)
		return
	}
	if err != nil {
		q.log.Debugf("Lease on cluster %s unreadable (not an observation): %s", cluster, err)
		return
	}
	q.core.observe(cluster, contentFromLease(lease), lease.ResourceVersion, now)
}

func (q *quorumLock) compositeRecord() *resourcelock.LeaderElectionRecord {
	holder, _, ok := q.core.majorityHolder()
	if !ok {
		holder = ""
	}
	return &resourcelock.LeaderElectionRecord{
		HolderIdentity:       holder,
		LeaseDurationSeconds: int(q.core.leaseDuration / time.Second),
	}
}

// Create exists for client-go's NotFound path, which this backend never triggers (Get never
// returns NotFound); it delegates to the same acquire flow defensively.
func (q *quorumLock) Create(ctx context.Context, ler resourcelock.LeaderElectionRecord) error {
	return q.Update(ctx, ler)
}

// Update is the write side of every client-go turn. The record's content is ours to compose —
// the term lives in the lease annotation and never transits through client-go; the passed
// record only discriminates a release (empty holder, client-go's ReleaseOnCancel shape) from an
// acquire/renew. Success means a majority of CAS writes landed in this batch; anything less is
// an error so the LeaderElector counts the turn as failed.
func (q *quorumLock) Update(ctx context.Context, ler resourcelock.LeaderElectionRecord) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	now := q.clock()

	if ler.HolderIdentity == "" {
		intents := q.core.releaseIntents(now)
		q.execute(ctx, intents, now)
		q.core.settle(now)
		return nil
	}

	wasLeader := q.core.phase == phaseLeader
	intents := q.core.step(now)
	if len(intents) == 0 && q.core.phase != phaseLeader {
		return xerrors.Errorf("no candidacy this turn (delaying or ineligible)")
	}
	q.execute(ctx, intents, now)
	if q.core.settle(now) {
		return nil
	}
	if wasLeader {
		return xerrors.Errorf("failed renewing the majority")
	}
	return xerrors.Errorf("no majority acquired")
}

// execute runs the CAS fan-out. A failed write means the object moved under us: read it back
// through immediately so the next turn works from fresh content and resourceVersion (without
// this, client-go's fast-path renewals — which skip Get — could never converge a straggler).
func (q *quorumLock) execute(ctx context.Context, intents []leaseWriteIntent, now time.Time) {
	for _, intent := range intents {
		lease := q.leaseForIntent(intent)
		err := q.transport.WriteLease(ctx, intent.Cluster, lease)
		if err != nil {
			q.log.Debugf("Lease CAS on cluster %s lost: %s", intent.Cluster, err)
		}
		q.core.applyWriteResult(intent, lease.ResourceVersion, err, now)
		if err != nil {
			q.readThrough(ctx, intent.Cluster, now)
		}
	}
}

// leaseForIntent materializes a CAS intent as the native Lease object, term in the annotation.
func (q *quorumLock) leaseForIntent(intent leaseWriteIntent) *coordinationv1.Lease {
	lease := &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{
			Name:            q.deployment.Name,
			Namespace:       q.deployment.Namespace,
			ResourceVersion: intent.ResourceVersion,
		},
	}
	holder := intent.Content.Holder
	duration := intent.Content.DurationSeconds
	lease.Spec.HolderIdentity = &holder
	lease.Spec.LeaseDurationSeconds = &duration
	if micros, err := strconv.ParseInt(intent.Content.RenewGeneration, 10, 64); err == nil {
		renewTime := metav1.NewMicroTime(time.UnixMicro(micros))
		lease.Spec.RenewTime = &renewTime
	}
	setLeaseTerm(lease, intent.Content.Term)
	return lease
}

// contentFromLease projects a read Lease onto the core's content record; it must invert
// leaseForIntent exactly, so written content reads back identical.
func contentFromLease(lease *coordinationv1.Lease) leaseContent {
	content := leaseContent{Exists: true}
	if lease.Spec.HolderIdentity != nil {
		content.Holder = *lease.Spec.HolderIdentity
	}
	if lease.Spec.LeaseDurationSeconds != nil {
		content.DurationSeconds = *lease.Spec.LeaseDurationSeconds
	}
	if lease.Spec.RenewTime != nil {
		content.RenewGeneration = strconv.FormatInt(lease.Spec.RenewTime.Time.UnixMicro(), 10)
	}
	content.Term, _ = leaseTerm(lease)
	return content
}

// Current is the elector-facing belief, pulled at snapshot time. isLeader == true implies the
// takeover hold-off has been served (the core's contract).
func (q *quorumLock) Current() (int64, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.core.current(q.clock())
}

// HoldOffRemaining is the time left before Current() starts answering leader after a dirty
// acquire; see quorumLeaseCore.holdOffRemaining.
func (q *quorumLock) HoldOffRemaining() time.Duration {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.core.holdOffRemaining(q.clock())
}

// ObserveTermFloor forwards the AC-stamped term (T16); see quorumLeaseCore.observeTermFloor.
func (q *quorumLock) ObserveTermFloor(floor int64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.core.observeTermFloor(floor)
}

// SetElectorate follows the CR's clusterSpecList; majority is always counted against it.
func (q *quorumLock) SetElectorate(clusters []string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.core.setElectorate(clusters)
}

func (q *quorumLock) RecordEvent(string) {}

func (q *quorumLock) Identity() string {
	return q.core.self
}

func (q *quorumLock) Describe() string {
	return fmt.Sprintf("%s/%s", q.deployment.Namespace, q.deployment.Name)
}
