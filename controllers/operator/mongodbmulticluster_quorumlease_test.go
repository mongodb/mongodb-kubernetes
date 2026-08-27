package operator

import (
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/xerrors"

	coordinationv1 "k8s.io/api/coordination/v1"
)

func TestLeaseTermRoundTrip(t *testing.T) {
	lease := &coordinationv1.Lease{}

	_, ok := leaseTerm(lease)
	assert.False(t, ok, "no annotations at all reads as no term")

	setLeaseTerm(lease, 7)
	term, ok := leaseTerm(lease)
	assert.True(t, ok)
	assert.Equal(t, int64(7), term)

	setLeaseTerm(lease, 8)
	term, _ = leaseTerm(lease)
	assert.Equal(t, int64(8), term, "restamping overwrites")
}

func TestLeaseTermPreservesOtherAnnotations(t *testing.T) {
	lease := &coordinationv1.Lease{}
	lease.Annotations = map[string]string{"unrelated": "kept"}

	setLeaseTerm(lease, 3)

	assert.Equal(t, "kept", lease.Annotations["unrelated"])
	assert.Equal(t, "3", lease.Annotations[leadershipTermAnnotation])
}

func TestLeaseTermMalformed(t *testing.T) {
	lease := &coordinationv1.Lease{}
	lease.Annotations = map[string]string{leadershipTermAnnotation: "not-a-number"}

	term, ok := leaseTerm(lease)
	assert.False(t, ok)
	assert.Equal(t, int64(0), term)
}

// ---- pure protocol core ----

const testLeaseDuration = 30 * time.Second

// coreForTest builds a core with a deterministic (zero) candidacy delay; tests that exercise the
// delay override randomDelay explicitly.
func coreForTest(self string) *quorumLeaseCore {
	core := newQuorumLeaseCore(self, testLeaseDuration)
	core.randomDelay = func() time.Duration { return 0 }
	core.setElectorate(clusters)
	return core
}

// fakeLeaseWorld is the shared lease store with real CAS semantics: an update succeeds only at
// the exact resourceVersion the intent was based on, a create only when the lease is absent.
type fakeLeaseWorld struct {
	leases map[string]*fakeLease
}

type fakeLease struct {
	content leaseContent
	rv      int
}

func newFakeLeaseWorld() *fakeLeaseWorld {
	return &fakeLeaseWorld{leases: map[string]*fakeLease{}}
}

func (w *fakeLeaseWorld) seed(cluster, holder string, term int64) {
	w.leases[cluster] = &fakeLease{
		content: leaseContent{Exists: true, Holder: holder, Term: term, DurationSeconds: 30, RenewGeneration: "g0"},
		rv:      1,
	}
}

// observeAll is the test stand-in for the backend's Get: every cluster's lease is read into the
// core (absent leases as the authoritative Exists=false observation).
func (w *fakeLeaseWorld) observeAll(core *quorumLeaseCore, now time.Time) {
	for _, cluster := range clusters {
		if l, ok := w.leases[cluster]; ok {
			core.observe(cluster, l.content, strconv.Itoa(l.rv), now)
		} else {
			core.observe(cluster, leaseContent{}, "", now)
		}
	}
}

func (w *fakeLeaseWorld) execute(core *quorumLeaseCore, intents []leaseWriteIntent, now time.Time) {
	for _, intent := range intents {
		l, exists := w.leases[intent.Cluster]
		if intent.Create {
			if exists {
				core.applyWriteResult(intent, "", xerrors.New("already exists"), now)
				continue
			}
			w.leases[intent.Cluster] = &fakeLease{content: intent.Content, rv: 1}
			core.applyWriteResult(intent, "1", nil, now)
			continue
		}
		if !exists || strconv.Itoa(l.rv) != intent.ResourceVersion {
			core.applyWriteResult(intent, "", xerrors.New("conflict"), now)
			continue
		}
		l.content = intent.Content
		l.rv++
		core.applyWriteResult(intent, strconv.Itoa(l.rv), nil, now)
	}
}

// drive is one full backend turn: read the world, step, execute, settle.
func (w *fakeLeaseWorld) drive(core *quorumLeaseCore, now time.Time) bool {
	w.observeAll(core, now)
	intents := core.step(now)
	w.execute(core, intents, now)
	return core.settle(now)
}

func TestQuorumCoreAcquiresOverAbsentLeases(t *testing.T) {
	t0 := time.Now()
	core := coreForTest(clusters[0])
	world := newFakeLeaseWorld()

	world.drive(core, t0) // arms the delay (absent leases are takeable immediately)
	require.True(t, world.drive(core, t0), "the second turn fires the fan-out and wins 3/3")

	for _, cluster := range clusters {
		assert.Equal(t, clusters[0], world.leases[cluster].content.Holder)
		assert.Equal(t, int64(1), world.leases[cluster].content.Term, "first term over a virgin world")
	}

	// a dirty acquire (nobody released) serves the full hold-off before guarded writes
	_, isLeader := core.current(t0)
	assert.False(t, isLeader, "hold-off not served yet")
	term, isLeader := core.current(t0.Add(testLeaseDuration))
	assert.True(t, isLeader)
	assert.Equal(t, int64(1), term)
}

func TestQuorumCoreRestartBlindness(t *testing.T) {
	t0 := time.Now()
	core := coreForTest(clusters[0])
	world := newFakeLeaseWorld()
	for _, cluster := range clusters {
		world.seed(cluster, "the-dead-leader", 7)
	}

	// the dead leader's content is frozen, but a fresh process's first read counts as a change:
	// one full duration blind before any death verdict or action
	world.drive(core, t0)
	world.drive(core, t0.Add(testLeaseDuration-time.Second))
	assert.Equal(t, phaseFollower, core.phase, "still blind")

	world.drive(core, t0.Add(testLeaseDuration+time.Second)) // arms the delay
	require.True(t, world.drive(core, t0.Add(testLeaseDuration+time.Second)))
	term, _ := core.current(t0.Add(2*testLeaseDuration + 2*time.Second))
	assert.Equal(t, int64(8), term, "term is max(observed)+1")
}

func TestQuorumCorePartialAcquireIsAbandoned(t *testing.T) {
	t0 := time.Now()
	core := coreForTest(clusters[0])
	world := newFakeLeaseWorld()
	for _, cluster := range clusters {
		world.seed(cluster, "the-dead-leader", 5)
	}

	expiredAt := t0.Add(testLeaseDuration + time.Second)
	world.drive(core, t0)

	// a concurrent candidate seizes two objects between our read and our write: CAS losses
	world.observeAll(core, expiredAt)
	intents := core.step(expiredAt)
	require.Len(t, intents, 3)
	world.seed(clusters[1], clusters[1], 6)
	world.seed(clusters[2], clusters[1], 6)
	world.leases[clusters[1]].rv = 2
	world.leases[clusters[2]].rv = 2
	world.execute(core, intents, expiredAt)
	assert.False(t, core.settle(expiredAt), "1 of 3 is not a majority")

	_, isLeader := core.current(expiredAt.Add(2 * testLeaseDuration))
	assert.False(t, isLeader, "a partial win never believes itself leader")

	// abandonment: the single win is left untouched — no renewal keeps it fresh, so it can
	// expire for everyone (renewing it would deadlock the 1-1 split forever)
	wonRV := world.leases[clusters[0]].rv
	for i := 1; i <= 3; i++ {
		world.drive(core, expiredAt.Add(time.Duration(i)*testLeaseDuration/3))
	}
	assert.Equal(t, wonRV, world.leases[clusters[0]].rv, "the partial win was never rewritten")
}

func TestQuorumCoreRenewalLossStopsGuardedWorkBeforeAnyRelease(t *testing.T) {
	t0 := time.Now()
	core := coreForTest(clusters[0])
	world := newFakeLeaseWorld()
	world.drive(core, t0)
	require.True(t, world.drive(core, t0))

	// two of three CAS targets moved under us (a candidate seized them): renewal counts 1/3
	world.observeAll(core, t0)
	intents := core.step(t0)
	for _, intent := range intents {
		assert.Equal(t, clusters[0], intent.Content.Holder, "a renewal batch never contains a release write")
	}
	world.leases[clusters[1]].rv++
	world.leases[clusters[2]].rv++
	world.execute(core, intents, t0)
	assert.False(t, core.settle(t0))

	// guarded work stops immediately; the leases were NOT released — that is the caller's later,
	// separate decision (stop first, step down second)
	_, isLeader := core.current(t0.Add(2 * testLeaseDuration))
	assert.False(t, isLeader)
	assert.Equal(t, clusters[0], world.leases[clusters[0]].content.Holder, "own lease still standing")
}

func TestQuorumCoreTermCollision(t *testing.T) {
	t0 := time.Now()
	world := newFakeLeaseWorld()
	for _, cluster := range clusters {
		world.seed(cluster, "the-dead-leader", 5)
	}
	coreA := coreForTest(clusters[0])
	coreB := coreForTest(clusters[1])

	expiredAt := t0.Add(testLeaseDuration + time.Second)
	world.drive(coreA, t0)
	world.drive(coreB, t0)
	world.drive(coreA, expiredAt) // both arm their delays
	world.drive(coreB, expiredAt)

	// both fire at the same instant with the same term (6); the shared objects' CAS serializes
	world.observeAll(coreA, expiredAt)
	world.observeAll(coreB, expiredAt)
	intentsA := coreA.step(expiredAt)
	intentsB := coreB.step(expiredAt)
	world.execute(coreA, intentsA, expiredAt)
	world.execute(coreB, intentsB, expiredAt)
	wonA := coreA.settle(expiredAt)
	wonB := coreB.settle(expiredAt)

	assert.True(t, wonA, "A's writes landed first")
	assert.False(t, wonB, "every one of B's CAS attempts hit a moved resourceVersion")
	for _, cluster := range clusters {
		assert.Equal(t, clusters[0], world.leases[cluster].content.Holder)
		assert.Equal(t, int64(6), world.leases[cluster].content.Term)
	}
}

func TestQuorumCoreCleanReleaseSkipsHoldOff(t *testing.T) {
	t0 := time.Now()
	world := newFakeLeaseWorld()
	leader := coreForTest(clusters[0])
	world.drive(leader, t0)
	require.True(t, world.drive(leader, t0))

	// clean step-down: guarded work is already stopped; zero every held lease
	world.observeAll(leader, t0)
	intents := leader.releaseIntents(t0)
	require.Len(t, intents, 3)
	world.execute(leader, intents, t0)
	leader.settle(t0)
	_, isLeader := leader.current(t0.Add(2 * testLeaseDuration))
	assert.False(t, isLeader)
	assert.Equal(t, int64(1), world.leases[clusters[0]].content.Term, "release keeps the term annotation for future candidacies")

	// the successor sees the release shape on a majority: takeable immediately, hold-off skipped
	successor := coreForTest(clusters[1])
	world.drive(successor, t0)
	require.True(t, world.drive(successor, t0))
	term, isLeader := successor.current(t0)
	assert.True(t, isLeader, "clean handover: no hold-off")
	assert.Equal(t, int64(2), term)
}

func TestQuorumCoreDirtyTakeoverServesHoldOff(t *testing.T) {
	t0 := time.Now()
	world := newFakeLeaseWorld()
	for _, cluster := range clusters {
		world.seed(cluster, "the-dead-leader", 3)
	}
	core := coreForTest(clusters[0])

	expiredAt := t0.Add(testLeaseDuration + time.Second)
	world.drive(core, t0)
	world.drive(core, expiredAt)
	require.True(t, world.drive(core, expiredAt))

	_, isLeader := core.current(expiredAt.Add(testLeaseDuration - time.Second))
	assert.False(t, isLeader, "dirty takeover: one full duration before the first guarded write")
	_, isLeader = core.current(expiredAt.Add(testLeaseDuration))
	assert.True(t, isLeader)
}

func TestQuorumCoreRandomizedDelayBounds(t *testing.T) {
	core := newQuorumLeaseCore(clusters[0], testLeaseDuration)
	for i := 0; i < 200; i++ {
		delay := core.randomDelay()
		assert.GreaterOrEqual(t, delay, time.Duration(0))
		assert.Less(t, delay, testLeaseDuration/3, "the delay never compresses the renewal cadence")
	}
}

func TestQuorumCoreTermFloor(t *testing.T) {
	t0 := time.Now()

	t.Run("candidacy starts above the floor", func(t *testing.T) {
		core := coreForTest(clusters[0])
		world := newFakeLeaseWorld()
		core.observeTermFloor(41)
		world.drive(core, t0)
		require.True(t, world.drive(core, t0))
		assert.Equal(t, int64(42), world.leases[clusters[0]].content.Term)
	})

	t.Run("a floor raise mid-leadership re-CASes the majority at floor+1", func(t *testing.T) {
		core := coreForTest(clusters[0])
		world := newFakeLeaseWorld()
		world.drive(core, t0)
		require.True(t, world.drive(core, t0))
		require.Equal(t, int64(1), core.heldTerm)

		core.observeTermFloor(9)
		require.True(t, world.drive(core, t0.Add(testLeaseDuration/3)), "the renewal keeps the majority at the bumped term")
		assert.Equal(t, int64(10), core.heldTerm)
		for _, cluster := range clusters {
			assert.Equal(t, int64(10), world.leases[cluster].content.Term)
		}
	})
}

func TestQuorumCoreMajorityIsOfConfiguredClusters(t *testing.T) {
	t0 := time.Now()

	t.Run("two visible of three configured is a majority", func(t *testing.T) {
		core := coreForTest(clusters[0])
		world := newFakeLeaseWorld()
		// the third cluster is unreachable: never observed at all, never counted, never CASed
		observeTwo := func(now time.Time) {
			for _, cluster := range clusters[:2] {
				if l, ok := world.leases[cluster]; ok {
					core.observe(cluster, l.content, strconv.Itoa(l.rv), now)
				} else {
					core.observe(cluster, leaseContent{}, "", now)
				}
			}
		}
		observeTwo(t0)
		core.step(t0) // arms the delay: two takeable ≥ majority of the configured three
		observeTwo(t0)
		intents := core.step(t0)
		require.Len(t, intents, 2)
		world.execute(core, intents, t0)
		assert.True(t, core.settle(t0), "2 of 3 configured is a majority")
	})

	t.Run("one visible of three configured never elects", func(t *testing.T) {
		core := coreForTest(clusters[0])
		core.observe(clusters[0], leaseContent{}, "", t0)
		core.step(t0)
		intents := core.step(t0.Add(testLeaseDuration))
		assert.Empty(t, intents, "a partitioned minority cannot even start a candidacy")
		assert.Equal(t, phaseFollower, core.phase)
	})
}

func TestQuorumCoreLeaderStepsDownOnObservedForeignMajority(t *testing.T) {
	t0 := time.Now()
	core := coreForTest(clusters[0])
	world := newFakeLeaseWorld()
	world.drive(core, t0)
	require.True(t, world.drive(core, t0))

	// a usurper's majority appears in the next read (we were partitioned; our CAS view is stale)
	world.seed(clusters[1], clusters[2], 4)
	world.seed(clusters[2], clusters[2], 4)
	world.observeAll(core, t0.Add(time.Second))

	_, isLeader := core.current(t0.Add(2 * testLeaseDuration))
	assert.False(t, isLeader, "an observed foreign majority dethrones immediately")
	assert.Equal(t, phaseFollower, core.phase)
}
