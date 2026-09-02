package operator

import (
	"fmt"
	"math/rand/v2"
	"sort"
	"strconv"
	"strings"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
)

// The election container is the native coordination.k8s.io/Lease: one Lease per cluster per
// deployment, named after the CR, in the CR's namespace. The native spec has no term field, so
// the term rides in an annotation — the whole object is still covered by the API server's CAS,
// and the record stays kubectl-visible. The annotation key is a frozen cross-track contract
// with the installer's RBAC matrix; never change it alone.
const leadershipTermAnnotation = "operator.mongodb.com/leadership-term"

// leaseTerm reads the leadership term off a Lease. An absent or malformed annotation reads as
// (0, false): a term-less lease (hand-written, or from a foreign writer) contributes no term to
// the max-observed-term candidacy math rather than poisoning it.
func leaseTerm(lease *coordinationv1.Lease) (int64, bool) {
	raw, ok := lease.Annotations[leadershipTermAnnotation]
	if !ok {
		return 0, false
	}
	term, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, false
	}
	return term, true
}

// setLeaseTerm stamps the leadership term, preserving any other annotations on the object.
func setLeaseTerm(lease *coordinationv1.Lease, term int64) {
	if lease.Annotations == nil {
		lease.Annotations = map[string]string{}
	}
	lease.Annotations[leadershipTermAnnotation] = strconv.FormatInt(term, 10)
}

// ---- pure protocol core ----
//
// quorumLeaseCore below is the whole majority-lease protocol with time as a parameter and zero I/O: observations
// are pushed in, CAS intents come out, write results are pushed back. The resourcelock backend
// owns the I/O; client-go's LeaderElector owns the timing loop. Everything is paced by ONE
// constant, leaseDuration: renewal cadence and the randomized candidacy delay are duration/3,
// expiry, restart blindness and the takeover hold-off are one full duration.

// leaseContent is one cluster's election lease content, as observed or as intended to be
// written. RenewGeneration is opaque: its only job is to change on every heartbeat; it is never
// compared against any clock (judging a writer's renewTime with the reader's clock is broken by
// construction under skew — 40s of skew would declare a healthy leader dead forever).
type leaseContent struct {
	Exists          bool
	Holder          string
	Term            int64
	DurationSeconds int32
	RenewGeneration string
}

// released reports client-go's clean-release shape — an empty holder with a 1-second duration.
// The previous leader zeroed the lease on purpose, so the new leader has no in-flight zombie
// write to wait out and may skip the takeover hold-off.
func (c leaseContent) released() bool {
	return c.Exists && c.Holder == "" && c.DurationSeconds == 1
}

// observationRecord is the per-lease expiry memory: (lastContent, observedAt), in memory only.
// observedAt advances only when the content CHANGES — if merely looking refreshed it, a dead
// leader would never expire. A fresh process starts with no records, so it is blind for one full
// duration before its first death verdict (restart blindness — the accepted cost that also
// disarms a returning zombie's own stale lease).
type observationRecord struct {
	content         leaseContent
	observedAt      time.Time
	resourceVersion string
}

// leaseWriteIntent is one CAS the core wants executed: create when the lease was authoritatively
// absent, otherwise an update conditioned on the resourceVersion the content was observed at.
// The API server's CAS is the only server-enforced piece of the protocol.
type leaseWriteIntent struct {
	Cluster         string
	Content         leaseContent
	Create          bool
	ResourceVersion string
}

type electionPhase int

const (
	phaseFollower electionPhase = iota
	phaseDelaying
	phaseLeader
)

// attemptKind records what the last batch of intents was for, so settle() knows which tally to
// run once the write results are in.
type attemptKind int

const (
	attemptNone attemptKind = iota
	attemptAcquire
	attemptRenew
	attemptRelease
)

// quorumLeaseCore is the pure core of the majority-lease election for ONE deployment.
// Leadership is a count, never a possession: a single CAS win only means "nobody touched this
// object between my read and my write"; majority (of the CONFIGURED electorate, never of the
// reachable subset) under one term is leadership. Split-brain is impossible because two
// majorities of 3 must share an object and that object's CAS serialized them.
//
// Candidacy is a single fan-out attempt: after a randomized delay (the 1-1-1 swap-cycle
// breaker), CAS every takeable lease at term max(observed, floor)+1 and count. A partial win is
// abandoned — never renewed — so it freezes and expires for everyone (renewing it would deadlock
// the 1-1 split forever). A failed attempt drops back to follower; re-eligibility needs the
// world to freeze for another full duration, which is the same-object-duel silence for free.
type quorumLeaseCore struct {
	self          string
	leaseDuration time.Duration
	randomDelay   func() time.Duration

	electorate []string
	records    map[string]*observationRecord
	termFloor  int64

	phase         electionPhase
	delayUntil    time.Time
	pending       attemptKind
	candidateTerm int64
	releasedSeen  int
	batchWins     int
	heldTerm      int64
	majorityHeld  bool
	acquiredAt    time.Time
	cleanAcquire  bool
}

func newQuorumLeaseCore(self string, leaseDuration time.Duration) *quorumLeaseCore {
	return &quorumLeaseCore{
		self:          self,
		leaseDuration: leaseDuration,
		randomDelay:   func() time.Duration { return rand.N(leaseDuration / 3) },
		records:       map[string]*observationRecord{},
	}
}

// setElectorate replaces the configured cluster list (the CR's clusterSpecList). Majority is
// always counted against this list: the startup client map may be short when a cluster was
// unreachable, and "majority of reachable" would let a partitioned minority elect itself.
func (s *quorumLeaseCore) setElectorate(clusters []string) {
	s.electorate = append([]string(nil), clusters...)
}

// observeTermFloor raises the candidacy floor (T16): the leader controller pushes the term
// stamped in the automation config after every snapshot, because the elector cannot read Ops
// Manager itself. A floor at or above the held term re-CASes the majority at floor+1 on the
// next renewal.
func (s *quorumLeaseCore) observeTermFloor(floor int64) {
	if floor > s.termFloor {
		s.termFloor = floor
	}
}

// observe records one cluster's lease as read. NotFound is an authoritative observation
// (content Exists=false); an unreachable cluster must NOT be observed at all — absence of
// visibility never advances or resets an expiry clock.
func (s *quorumLeaseCore) observe(cluster string, content leaseContent, resourceVersion string, now time.Time) {
	rec := s.records[cluster]
	if rec == nil {
		rec = &observationRecord{}
		s.records[cluster] = rec
		rec.content = content
		rec.observedAt = now
	} else if rec.content != content {
		rec.content = content
		rec.observedAt = now
	}
	rec.resourceVersion = resourceVersion

	// a leader that lost its majority to someone else steps out of guarded work immediately;
	// its residual leases are left to expire (the ghost is absorbed lazily)
	if s.phase == phaseLeader {
		if holder, _, ok := s.majorityHolder(); ok && holder != s.self {
			s.stepDown()
		}
	}
}

func (s *quorumLeaseCore) stepDown() {
	s.phase = phaseFollower
	s.heldTerm = 0
	s.majorityHeld = false
	s.cleanAcquire = false
}

func (s *quorumLeaseCore) majority() int {
	return len(s.electorate)/2 + 1
}

// effectiveDuration prefers the duration carried in the object (changeable without redeploy)
// over the configured one; the release shape's 1s is honored like any other value.
func (s *quorumLeaseCore) effectiveDuration(c leaseContent) time.Duration {
	if c.Exists && c.DurationSeconds > 0 {
		return time.Duration(c.DurationSeconds) * time.Second
	}
	return s.leaseDuration
}

// expired is the own-clock expiry verdict: the content has not changed for a full duration.
func (s *quorumLeaseCore) expired(rec *observationRecord, now time.Time) bool {
	return now.Sub(rec.observedAt) > s.effectiveDuration(rec.content)
}

// takeable reports whether a takeover CAS on this lease is permitted: authoritatively absent,
// voluntarily emptied, or expired per MY observation record. A never-observed lease is not
// takeable — that is absence of visibility, not absence of a holder.
func (s *quorumLeaseCore) takeable(cluster string, now time.Time) bool {
	rec := s.records[cluster]
	if rec == nil {
		return false
	}
	if !rec.content.Exists {
		return true
	}
	if rec.content.Holder == "" {
		return true
	}
	return s.expired(rec, now)
}

// majorityHolder returns the holder carried by a majority of the electorate's leases under one
// term, if any. Released and absent leases count for nobody.
func (s *quorumLeaseCore) majorityHolder() (string, int64, bool) {
	type holderTerm struct {
		holder string
		term   int64
	}
	counts := map[holderTerm]int{}
	for _, cluster := range s.electorate {
		rec := s.records[cluster]
		if rec == nil || !rec.content.Exists || rec.content.Holder == "" {
			continue
		}
		counts[holderTerm{rec.content.Holder, rec.content.Term}]++
	}
	for ht, n := range counts {
		if n >= s.majority() {
			return ht.holder, ht.term, true
		}
	}
	return "", 0, false
}

// contentFingerprint is a deterministic serialization of the electorate's observed lease
// contents — the composite record's raw bytes. It changes iff some lease's content changed, so
// client-go's observedTime expiry over it is exactly the quorum-level frozen-content verdict.
func (s *quorumLeaseCore) contentFingerprint() []byte {
	clusters := append([]string(nil), s.electorate...)
	sort.Strings(clusters)
	var b strings.Builder
	for _, cluster := range clusters {
		rec := s.records[cluster]
		if rec == nil {
			fmt.Fprintf(&b, "%s=unobserved;", cluster)
			continue
		}
		c := rec.content
		fmt.Fprintf(&b, "%s=%t/%s/%d/%d/%s;", cluster, c.Exists, c.Holder, c.Term, c.DurationSeconds, c.RenewGeneration)
	}
	return []byte(b.String())
}

// maxObservedTerm is the candidacy input: the highest term seen anywhere, takeable or not.
// observedTerm is the highest leadership term this core has seen anywhere — held, pushed as a
// floor, or carried by any lease it observed — independent of whether we lead. Member
// controllers fence stale directives against it: an instruction from an older leadership must
// be refused even by a cluster that never led.
func (s *quorumLeaseCore) observedTerm() int64 {
	return max(s.maxObservedTerm(), s.termFloor, s.heldTerm)
}

func (s *quorumLeaseCore) maxObservedTerm() int64 {
	var maxTerm int64
	for _, rec := range s.records {
		if rec.content.Exists && rec.content.Term > maxTerm {
			maxTerm = rec.content.Term
		}
	}
	return maxTerm
}

// selfEligible: only a member of the current electorate runs for leadership (a cluster being
// drained doesn't run).
func (s *quorumLeaseCore) selfEligible() bool {
	for _, cluster := range s.electorate {
		if cluster == s.self {
			return true
		}
	}
	return false
}

// step advances the state machine one turn and returns the CAS intents to execute. The caller
// executes them, feeds each result back through applyWriteResult, then calls settle.
func (s *quorumLeaseCore) step(now time.Time) []leaseWriteIntent {
	switch s.phase {
	case phaseLeader:
		return s.renewIntents(now)
	case phaseFollower:
		if !s.selfEligible() {
			return nil
		}
		if s.takeableCount(now) < s.majority() {
			return nil
		}
		// the expiry predicate fired: arm the randomized delay before the first CAS —
		// Raft's randomized election timeout, breaking the 1-1-1 swap-cycle symmetry.
		// A zero delay proceeds this same turn (falling through), never costing an extra tick.
		s.phase = phaseDelaying
		s.delayUntil = now.Add(s.randomDelay())
		fallthrough
	case phaseDelaying:
		if s.takeableCount(now) < s.majority() {
			s.phase = phaseFollower // the world moved on (a leader renewed); stand down
			return nil
		}
		if now.Before(s.delayUntil) {
			return nil
		}
		return s.acquireIntents(now)
	}
	return nil
}

func (s *quorumLeaseCore) takeableCount(now time.Time) int {
	n := 0
	for _, cluster := range s.electorate {
		if s.takeable(cluster, now) {
			n++
		}
	}
	return n
}

// acquireIntents is the single candidacy fan-out: pick term max(observed, floor)+1 and CAS
// every takeable lease. The released-lease count is remembered so settle can recognize a clean
// handover on a majority and skip the hold-off.
func (s *quorumLeaseCore) acquireIntents(now time.Time) []leaseWriteIntent {
	s.candidateTerm = max(s.maxObservedTerm(), s.termFloor) + 1
	s.pending = attemptAcquire
	s.releasedSeen = 0
	s.batchWins = 0
	var intents []leaseWriteIntent
	for _, cluster := range s.electorate {
		if !s.takeable(cluster, now) {
			continue
		}
		rec := s.records[cluster]
		if rec.content.released() {
			s.releasedSeen++
		}
		intents = append(intents, leaseWriteIntent{
			Cluster:         cluster,
			Content:         s.heldContent(s.candidateTerm, now),
			Create:          !rec.content.Exists,
			ResourceVersion: rec.resourceVersion,
		})
	}
	return intents
}

// renewIntents re-proves the majority (~every duration/3): rewrite every lease that is mine, and
// converge any takeable straggler so a later loss of one majority member is survivable. A fresh
// foreign lease (a ghost) is left alone. A raise STRICTLY above the held term — a pushed floor
// or a higher term observed in any lease (an abandoned candidacy's lingering minority write) —
// bumps the renewal to raise+1, re-CASing the whole majority in one pass. Equality never bumps:
// the leader stamps its own held term into the AC, and treating its own stamp as a raise made
// every AC write inflate the term and skew the leases ahead of the directives (found live).
func (s *quorumLeaseCore) renewIntents(now time.Time) []leaseWriteIntent {
	renewTerm := s.heldTerm
	if raise := max(s.termFloor, s.maxObservedTerm()); raise > s.heldTerm {
		renewTerm = raise + 1
	}
	s.pending = attemptRenew
	s.candidateTerm = renewTerm
	s.batchWins = 0
	var intents []leaseWriteIntent
	for _, cluster := range s.electorate {
		rec := s.records[cluster]
		if rec == nil {
			continue
		}
		mine := rec.content.Exists && rec.content.Holder == s.self
		if !mine && !s.takeable(cluster, now) {
			continue
		}
		intents = append(intents, leaseWriteIntent{
			Cluster:         cluster,
			Content:         s.heldContent(renewTerm, now),
			Create:          !rec.content.Exists,
			ResourceVersion: rec.resourceVersion,
		})
	}
	return intents
}

// releaseIntents is the clean step-down: zero every lease that is mine (client-go's release
// shape), keeping the term annotation so future candidacies still observe it. Guarded work must
// already be stopped when this is called — release comes last, never first.
func (s *quorumLeaseCore) releaseIntents(now time.Time) []leaseWriteIntent {
	s.pending = attemptRelease
	var intents []leaseWriteIntent
	for cluster, rec := range s.records {
		if !rec.content.Exists || rec.content.Holder != s.self {
			continue
		}
		intents = append(intents, leaseWriteIntent{
			Cluster: cluster,
			Content: leaseContent{
				Exists:          true,
				Holder:          "",
				Term:            rec.content.Term,
				DurationSeconds: 1,
				RenewGeneration: renewGeneration(now),
			},
			ResourceVersion: rec.resourceVersion,
		})
	}
	sort.Slice(intents, func(i, j int) bool { return intents[i].Cluster < intents[j].Cluster })
	return intents
}

func (s *quorumLeaseCore) heldContent(term int64, now time.Time) leaseContent {
	return leaseContent{
		Exists:          true,
		Holder:          s.self,
		Term:            term,
		DurationSeconds: int32(s.leaseDuration / time.Second),
		RenewGeneration: renewGeneration(now),
	}
}

// renewGeneration derives the heartbeat carrier from the caller's clock; observers only ever
// compare it for change. Microsecond precision, because the carrier round-trips through
// LeaseSpec.RenewTime (a metav1.MicroTime) and written content must read back identical.
func renewGeneration(now time.Time) string {
	return strconv.FormatInt(now.UnixMicro(), 10)
}

// applyWriteResult feeds one executed intent back. A successful CAS is a fresh observation of
// the content we wrote; a failed one changes nothing in the record — the next read refreshes it.
// Tallies count only this batch's landed writes, never lingering beliefs: a failed CAS means the
// object moved under us and the record is provably stale.
func (s *quorumLeaseCore) applyWriteResult(intent leaseWriteIntent, resourceVersion string, err error, now time.Time) {
	if err != nil {
		return
	}
	if intent.Content.Holder == s.self {
		s.batchWins++
	}
	s.records[intent.Cluster] = &observationRecord{
		content:         intent.Content,
		observedAt:      now,
		resourceVersion: resourceVersion,
	}
}

// settle runs the tally for the batch that just executed and moves the phase accordingly.
// It reports whether the batch achieved its goal (acquired or kept a majority; releases always
// settle true).
func (s *quorumLeaseCore) settle(now time.Time) bool {
	pending := s.pending
	s.pending = attemptNone
	switch pending {
	case attemptAcquire:
		if s.batchWins >= s.majority() {
			s.phase = phaseLeader
			s.heldTerm = s.candidateTerm
			s.majorityHeld = true
			s.acquiredAt = now
			s.cleanAcquire = s.releasedSeen >= s.majority()
			return true
		}
		// abandonment: partial wins stay in place and expire on their own; renewing them
		// would keep them fresh for everyone else and deadlock the 1-1 split
		s.phase = phaseFollower
		return false
	case attemptRenew:
		s.heldTerm = s.candidateTerm
		s.majorityHeld = s.batchWins >= s.majority()
		// majority lost: guarded work stops NOW (current() reads false); the release write is
		// the caller's later, separate decision — stop first, step down second
		return s.majorityHeld
	case attemptRelease:
		s.stepDown()
		return true
	}
	return false
}

// current is the elector's outward belief. isLeader == true implies the takeover hold-off has
// been served: one full duration after a dirty acquire, immediately after a clean one — sized so
// a zombie's in-flight write lands before our first guarded write.
func (s *quorumLeaseCore) current(now time.Time) (int64, bool) {
	if s.phase != phaseLeader || !s.majorityHeld {
		return 0, false
	}
	if !s.cleanAcquire && now.Sub(s.acquiredAt) < s.leaseDuration {
		return 0, false
	}
	return s.heldTerm, true
}

// holdOffRemaining is how long current() will keep answering non-leader after a dirty acquire:
// positive only while leading with an unserved hold-off, zero otherwise. Current() flipping true
// at the end of the hold-off is a silent transition — client-go reported the acquire when it
// happened — so the elector schedules one extra wake-up at this horizon.
func (s *quorumLeaseCore) holdOffRemaining(now time.Time) time.Duration {
	if s.phase != phaseLeader || s.cleanAcquire {
		return 0
	}
	if remaining := s.leaseDuration - now.Sub(s.acquiredAt); remaining > 0 {
		return remaining
	}
	return 0
}
