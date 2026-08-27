package operator

import (
	"fmt"
	"maps"
	"sort"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	mdbmultiv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdbmulti"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
	operatorv1 "github.com/mongodb/mongodb-kubernetes/api/operator/v1"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/construct/scalers"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/construct/scalers/interfaces"
	"github.com/mongodb/mongodb-kubernetes/pkg/dns"
	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/scale"
)

// This file is the leader's pure planner: plan(plannerSnapshot) -> planDecision. No I/O, no
// clock reads, no memory between calls — the actuator (the leader reconciler) assembles the
// snapshot from live reads, executes exactly one decision per pass and maps it one-to-one to a
// workflow.Status. Any decision reproduces in a table test by printing the snapshot that
// produced it.
//
// Wait -> supplier table (every wait must key on a fact whose supplier is not blocked by that
// wait — the sequencing lesson this design encodes):
//
//	AC write             waits on agentRegistered           supplier: member + agent, needs only the grant
//	next cluster's step  waits on inGoalState               suppliers: AC write + agents, not gated on it
//	scale-down advance   waits on OM-witnessed convergence  supplier: agents -> OM, member not needed
//	                     (the shrinking member's own facts go transiently false, so the leader
//	                      witnesses convergence via Ops Manager directly)

// plannerNotProgressingAfter is how long a cluster may sit mid-step before the status message
// flips from "waiting" to "not progressing". It gates a message, never safety.
const plannerNotProgressingAfter = 5 * time.Minute

// plannerSnapshot is everything plan() may look at, fully serializable.
type plannerSnapshot struct {
	Now            time.Time
	LeadershipTerm int64
	Name           string // CR name = replica-set name = process-name prefix
	Namespace      string
	SpecHash       string
	ProjectID      string
	ClusterDomain  string
	Targets        []clusterTarget          // spec order
	SpecViolations []string                 // decentralizedSpecViolations(spec), precomputed so plan() stays spec-free
	Directives     map[string]directiveView // by clusterName; includes removed-from-spec ones
	AC             acView
	OMFacts        omFactsView
}

type clusterTarget struct {
	ClusterName    string
	Members        int
	ExternalDomain *string
}

type directiveView struct {
	Exists      bool
	Unreachable bool // the read FAILED (not NotFound): absence of visibility, not absence of entry
	Spec        operatorv1.MongoDBDirectiveSpec
	Status      operatorv1.MongoDBDirectiveStatus
	Generation  int64
}

// acView is the deployment read back from Ops Manager this pass, reduced to what plan() keys on.
type acView struct {
	Read                bool
	LeadershipTerm      int64       // 0 when never stamped
	SpecHash            string      // spec hash the deployment content was last built from; "" when never stamped
	MemberCountsByIndex map[int]int // cluster index -> live process count for this replica set
}

// omFactsView is the leader's witness for agent-plane facts, read from Ops Manager.
type omFactsView struct {
	Read          bool
	ProcessStates map[string]processFactView // by hostname
}

type processFactView struct {
	Registered   bool // present and not stale
	GoalAchieved bool // GoalVersionAchieved >= GoalVersion
}

type planDecisionKind int

const (
	decisionNoop planDecisionKind = iota
	decisionWriteDirective // create-or-update ONE directive's spec (allocation push or advancement)
	decisionWriteAC        // publish membership at these counts, term attached
	decisionInvalidSpec    // terminal until the USER edits the spec -> workflow.Failed
	decisionNotProgressing // legal spec, waiting on the world -> workflow.Pending
)

func (k planDecisionKind) String() string {
	switch k {
	case decisionNoop:
		return "Noop"
	case decisionWriteDirective:
		return "WriteDirective"
	case decisionWriteAC:
		return "WriteAC"
	case decisionInvalidSpec:
		return "InvalidSpec"
	case decisionNotProgressing:
		return "NotProgressing"
	}
	return "Unknown"
}

type acPayload struct {
	LeadershipTerm int64
	MemberCounts   map[string]int // clusterName -> process count to publish
}

type planDecision struct {
	Kind          planDecisionKind
	TargetCluster string                          // for WriteDirective
	DirectiveSpec operatorv1.MongoDBDirectiveSpec // the FULL desired spec — declarative PUT
	AC            *acPayload                      // for WriteAC
	Reason        string                          // prose for the CR status message and logs
}

// plan is an ordered guard list: first match wins, and the order IS the priority scheme between
// actions — the correctness-critical artifact, table-tested as such.
func plan(s plannerSnapshot) planDecision {
	if d, ok := staleWorld(s); ok {
		return d
	}
	if err := validate(s); err != nil {
		return planDecision{Kind: decisionInvalidSpec, Reason: err.Error()}
	}
	if names := removedFromSpec(s); len(names) > 0 {
		return planDecision{Kind: decisionInvalidSpec, Reason: fmt.Sprintf("cluster removal is not implemented in the POC; clusters with a directive but absent from the spec: %s", strings.Join(names, ", "))}
	}
	if d, ok := allocation(s); ok {
		return d
	}
	if d, ok := recognition(s); ok {
		return d
	}
	if d, ok := advancement(s); ok {
		return d
	}
	if d, ok := orphanedACMembers(s); ok {
		return d
	}
	if payload, ok := acWriteNeeded(s); ok {
		return planDecision{Kind: decisionWriteAC, AC: &payload, Reason: "publishing the automation config membership"}
	}
	if d, ok := notProgressing(s); ok {
		return d
	}
	return planDecision{Kind: decisionNoop, Reason: "all clusters in goal state at the current spec"}
}

// staleWorld refuses to act — or even judge the spec — when anything carries a newer leadership
// term than ours: a legitimate newer leader exists and the lease layer will depose us.
func staleWorld(s plannerSnapshot) (planDecision, bool) {
	if s.AC.Read && s.AC.LeadershipTerm > s.LeadershipTerm {
		return planDecision{Kind: decisionNotProgressing, Reason: fmt.Sprintf("the automation config carries a newer leadership term %d than the locally observed term %d", s.AC.LeadershipTerm, s.LeadershipTerm)}, true
	}
	for _, name := range sortedClusterNames(s.Directives) {
		if d := s.Directives[name]; d.Exists && d.Spec.LeadershipTerm > s.LeadershipTerm {
			return planDecision{Kind: decisionNotProgressing, Reason: fmt.Sprintf("the directive on cluster %s carries a newer leadership term %d than the locally observed term %d", name, d.Spec.LeadershipTerm, s.LeadershipTerm)}, true
		}
	}
	return planDecision{}, false
}

// validate refuses specs the decentralized pair cannot deliver honestly (SpecViolations — an
// unguarded TLS spec would wedge pods on a pem secret nobody creates), then any spec where one
// cluster scales up while another scales down (relative to the granted counts): the scale
// direction decides the whole pass ordering (AC-first vs grant-first), so a mixed direction is
// unplannable. Scalers are built over spec clusters only — a removed-from-spec directive is the
// next guard's job, with its own message.
func validate(s plannerSnapshot) error {
	if len(s.SpecViolations) > 0 {
		return fmt.Errorf("not supported in the decentralized POC: %s", strings.Join(s.SpecViolations, ", "))
	}
	return blockScalingBothWays(plannerScalers(s))
}

// decentralizedSpecViolations lists the spec features the decentralized pair must refuse rather
// than half-apply. Each entry is receipt-backed: TLS would mount a pem secret nobody creates and
// publish an empty certificateKeyFile; auth, internal-cluster auth and roles have no
// updateOmAuthentication/ensureRoles counterpart; backup was cut from M3.8. Features the legacy
// multi-cluster controller does not support either (prometheus, vault) are NOT refused — same
// silent no-op as legacy.
func decentralizedSpecViolations(spec mdbmultiv1.MongoDBMultiSpec) []string {
	var violations []string
	security := spec.GetSecurity()
	if security.IsTLSEnabled() {
		violations = append(violations, "spec.security.tls (also implied by certsSecretPrefix)")
	}
	if security.Authentication != nil && security.Authentication.Enabled {
		violations = append(violations, "spec.security.authentication")
	}
	if security.GetInternalClusterAuthenticationMode() != "" {
		violations = append(violations, "spec.security.authentication.internalCluster")
	}
	if len(security.Roles) > 0 || len(security.RoleRefs) > 0 {
		violations = append(violations, "spec.security.roles/roleRefs")
	}
	if spec.Backup != nil && spec.Backup.Mode == "enabled" {
		violations = append(violations, "spec.backup")
	}
	return violations
}

func plannerScalers(s plannerSnapshot) []interfaces.MultiClusterReplicaSetScaler {
	specList := make(mdbv1.ClusterSpecList, 0, len(s.Targets))
	for _, t := range s.Targets {
		specList = append(specList, mdbv1.ClusterSpecItem{ClusterName: t.ClusterName, Members: t.Members})
	}
	prevMembers := grantedMemberClusters(s)
	result := make([]interfaces.MultiClusterReplicaSetScaler, 0, len(s.Targets))
	for _, t := range s.Targets {
		result = append(result, scalers.NewMultiClusterReplicaSetScaler("mongodbmulticluster", specList, t.ClusterName, allocatedIndex(s, t.ClusterName), prevMembers))
	}
	return result
}

// grantedMemberClusters expresses "current" for the scaling machinery: the counts the directives
// GRANT (what the leader last instructed), never the CR spec and never live StatefulSet reads.
// Ordered by allocated index so the scalers' first-mismatch throttling and our own
// first-behind-spec selection agree on who moves.
func grantedMemberClusters(s plannerSnapshot) []multicluster.MemberCluster {
	members := make([]multicluster.MemberCluster, 0, len(s.Targets))
	for _, t := range s.Targets {
		members = append(members, multicluster.MemberCluster{Name: t.ClusterName, Index: allocatedIndex(s, t.ClusterName), Replicas: granted(s, t.ClusterName)})
	}
	sort.SliceStable(members, func(i, j int) bool { return members[i].Index < members[j].Index })
	return members
}

// granted is what the leader last instructed — and, for a cluster whose directive is absent,
// what the observed world proves was instructed: the AC count at its index. The seed rule
// (Failure 7): state loss may cost discipline, never capacity — absent coordination state must
// never read as an instruction to scale to 0. The seed only follows a KNOWN index: under total
// map loss allocatedIndex would guess the spec position, and a reordered spec would read
// another cluster's count — no seed is better than a guessed one (the mint-collision refusal
// owns that world and names the runbook).
func granted(s plannerSnapshot, clusterName string) int {
	if d, ok := s.Directives[clusterName]; ok && d.Exists {
		return d.Spec.MemberCount
	}
	if s.AC.Read {
		if index, ok := visibleAllocations(s)[clusterName]; ok {
			return s.AC.MemberCountsByIndex[index]
		}
	}
	return 0
}

// clusterStatusOption reports each spec cluster's granted member count — what the leader last
// instructed, the closest honest "materialized" number it owns. Attached to every status write:
// the list carries facts, not judgment, so it rides Failed and Pending too.
func clusterStatusOption(s plannerSnapshot) status.Option {
	items := make([]status.ClusterStatusItem, 0, len(s.Targets))
	total := 0
	for _, t := range s.Targets {
		g := granted(s, t.ClusterName)
		items = append(items, status.ClusterStatusItem{ClusterName: t.ClusterName, Members: g})
		total += g
	}
	return status.MultiReplicaSetMemberOption{Members: total, ClusterStatusList: items}
}

// allocatedIndex resolves a cluster's index from the visible allocations, falling back to the
// spec position for a cluster not allocated yet (only ordering decisions read the fallback; a
// directive is never written from it — the allocation guard runs first).
func allocatedIndex(s plannerSnapshot, clusterName string) int {
	if idx, ok := visibleAllocations(s)[clusterName]; ok {
		return idx
	}
	for i, t := range s.Targets {
		if t.ClusterName == clusterName {
			return i
		}
	}
	return -1
}

func removedFromSpec(s plannerSnapshot) []string {
	inSpec := make(map[string]struct{}, len(s.Targets))
	for _, t := range s.Targets {
		inSpec[t.ClusterName] = struct{}{}
	}
	var names []string
	for _, name := range sortedClusterNames(s.Directives) {
		if _, ok := inSpec[name]; !ok && s.Directives[name].Exists {
			names = append(names, name)
		}
	}
	return names
}

// visibleAllocations unions the allocation maps of every readable directive spec. A ghost — an
// entry a dead leader pushed to a single copy — is a visible entry too: every visible entry
// consumes its index, so two acted-on entries can never conflict. On a key conflict between
// copies (only reachable through sub-quorum ghosts) the value present on more copies wins, ties
// to the lower index — deterministic, and the loser provably never acted.
func visibleAllocations(s plannerSnapshot) map[string]int {
	votes := map[string]map[int]int{}
	for _, d := range s.Directives {
		if !d.Exists {
			continue
		}
		for cluster, index := range d.Spec.IndexAllocations {
			if votes[cluster] == nil {
				votes[cluster] = map[int]int{}
			}
			votes[cluster][index]++
		}
	}
	result := make(map[string]int, len(votes))
	for cluster, indexVotes := range votes {
		bestIndex, bestVotes := -1, 0
		for index, count := range indexVotes {
			if count > bestVotes || (count == bestVotes && index < bestIndex) {
				bestIndex, bestVotes = index, count
			}
		}
		result[cluster] = bestIndex
	}
	return result
}

// desiredAllocations extends the visible map with a mint for every spec cluster missing an
// entry: never mint for a key with a visible entry — reuse it (a re-added cluster gets its old
// index back); a new index is max over ALL visible entries + 1.
func desiredAllocations(s plannerSnapshot) map[string]int {
	desired := visibleAllocations(s)
	nextIndex := 0
	for _, index := range desired {
		if index >= nextIndex {
			nextIndex = index + 1
		}
	}
	for _, t := range s.Targets {
		if _, ok := desired[t.ClusterName]; !ok {
			desired[t.ClusterName] = nextIndex
			nextIndex++
		}
	}
	return desired
}

// allocation ensures every spec cluster has an index whose entry is durable — visible on at
// least min(2, N) directive specs. An API server returning the object is the durability receipt
// (committed in that etcd); members never consume the map, so durability needs 2 reachable API
// servers, not 2 live operators. Allocation IS the directive write: the map rides every spec.
func allocation(s plannerSnapshot) (planDecision, bool) {
	visible := visibleAllocations(s)
	mintNeeded := false
	for _, t := range s.Targets {
		if _, ok := visible[t.ClusterName]; !ok {
			mintNeeded = true
		}
	}
	desired := desiredAllocations(s)
	// minting while a copy is unreachable risks a sub-quorum ghost on the copy we cannot see;
	// pushing an already-visible map is still allowed
	if mintNeeded {
		for _, name := range sortedClusterNames(s.Directives) {
			if s.Directives[name].Unreachable {
				return planDecision{Kind: decisionNotProgressing, Reason: fmt.Sprintf("cannot allocate cluster indexes while cluster %s is unreachable", name)}, true
			}
		}
		// a mint must also be checked against the automation config: an index the AC already
		// carries members at belongs to a lost allocation map, and minting over it would create
		// colliding process identities — refuse and name the recovery instead
		if !s.AC.Read {
			return planDecision{Kind: decisionNotProgressing, Reason: "cannot mint cluster indexes while the automation config is unreadable"}, true
		}
		for _, t := range s.Targets {
			if _, ok := visible[t.ClusterName]; ok {
				continue
			}
			if count := s.AC.MemberCountsByIndex[desired[t.ClusterName]]; count > 0 {
				return planDecision{Kind: decisionNotProgressing, Reason: fmt.Sprintf("refusing to mint index %d for cluster %s: the automation config already carries %d members at that index and no surviving directive claims it; recover by writing one directive carrying the recovered index allocations (majority-loss runbook)", desired[t.ClusterName], t.ClusterName, count)}, true
			}
		}
	}

	ackThreshold := min(2, len(s.Targets))
	for _, t := range s.Targets {
		index := desired[t.ClusterName]
		acks := 0
		for _, d := range s.Directives {
			if d.Exists {
				if stored, ok := d.Spec.IndexAllocations[t.ClusterName]; ok && stored == index {
					acks++
				}
			}
		}
		if acks >= ackThreshold {
			continue
		}
		// push the map to the first spec cluster (by allocated index) whose readable directive
		// is missing or disagreeing with the entry; a created directive is seeded at granted() —
		// 0 for a brand-new cluster (advancement raises the count later), the AC count for a
		// cluster whose directive was lost over live members (the seed rule). Known window: a
		// grant lost mid-FIRST-deploy (AC still empty) reseeds at 0 and the parallel first-deploy
		// advancement re-raises it — accepted, no members exist in the AC to protect
		for _, target := range targetsByAllocatedIndex(s, desired) {
			d := s.Directives[target.ClusterName]
			if d.Unreachable {
				continue
			}
			stored, ok := d.Spec.IndexAllocations[t.ClusterName]
			if d.Exists && ok && stored == index {
				continue
			}
			// creating a directive needs the seed count, and the seed is the AC count — an
			// unreadable AC means an unknown seed, never a 0
			if !d.Exists && !s.AC.Read {
				return planDecision{Kind: decisionNotProgressing, Reason: fmt.Sprintf("cannot create the directive on cluster %s while the automation config is unreadable: the seed member count is unknown", target.ClusterName)}, true
			}
			// the push must never re-publish a damaged below-AC grant (the wedge) while carrying
			// a map change: floor the count at the AC count at the cluster's desired index —
			// during the normal ladders granted >= acCount, so this is a no-op
			count := granted(s, target.ClusterName)
			if acAtIndex := s.AC.MemberCountsByIndex[desired[target.ClusterName]]; s.AC.Read && acAtIndex > count {
				count = acAtIndex
			}
			spec := desiredDirectiveSpec(s, target.ClusterName, count, desired)
			return planDecision{
				Kind:          decisionWriteDirective,
				TargetCluster: target.ClusterName,
				DirectiveSpec: spec,
				Reason:        fmt.Sprintf("recording the index allocation for cluster %s on cluster %s (%d/%d copies)", t.ClusterName, target.ClusterName, acks, ackThreshold),
			}, true
		}
		return planDecision{Kind: decisionNotProgressing, Reason: fmt.Sprintf("the index allocation for cluster %s is on %d/%d reachable copies and no reachable directive can take it", t.ClusterName, acks, ackThreshold)}, true
	}
	return planDecision{}, false
}

// recognition re-grants coordination state the observed world proves existed: a spec cluster
// whose directive is missing — or granted below — the AC count at its index is re-granted AT
// the AC count (the seed rule). Recognition of existing capacity, not a rollout step: it
// changes nothing physical, so it is safe while another cluster is mid-ladder and safe in
// parallel. granted < acCount can only mean damage — scale-up keeps the grant ahead of the AC
// and scale-down keeps the AC at or below the grant — so a mid-scale-down loss reseeds at the
// down-ladder's own next value and the shrink completes as if never interrupted.
func recognition(s plannerSnapshot) (planDecision, bool) {
	if !s.AC.Read {
		return planDecision{}, false // cannot read what exists; notProgressing reports it
	}
	allocations := desiredAllocations(s)
	for _, t := range targetsByAllocatedIndex(s, allocations) {
		d := s.Directives[t.ClusterName]
		if d.Unreachable {
			continue
		}
		ac := acCount(s, t.ClusterName)
		if ac == 0 {
			continue
		}
		if !d.Exists || d.Spec.MemberCount < ac {
			return planDecision{
				Kind:          decisionWriteDirective,
				TargetCluster: t.ClusterName,
				DirectiveSpec: desiredDirectiveSpec(s, t.ClusterName, ac, allocations),
				Reason:        fmt.Sprintf("recognizing existing capacity: re-granting cluster %s at the automation config count %d", t.ClusterName, ac),
			}, true
		}
	}
	return planDecision{}, false
}

func desiredDirectiveSpec(s plannerSnapshot, clusterName string, memberCount int, allocations map[string]int) operatorv1.MongoDBDirectiveSpec {
	return operatorv1.MongoDBDirectiveSpec{
		ClusterName:      clusterName,
		LeadershipTerm:   s.LeadershipTerm,
		TargetSpecHash:   s.SpecHash,
		MemberCount:      memberCount,
		ClusterIndex:     allocations[clusterName],
		IndexAllocations: maps.Clone(allocations),
		ProjectID:        s.ProjectID,
		AdvancedAt:       metav1.NewTime(s.Now),
	}
}

// isFirstDeployment is the deployment-wide analog of the scalers' ScalingFirstTime: nothing in
// the automation config yet means no replica-set quorum to protect, so directives advance in
// parallel at full count. Keying on the AC (not on the granted counts) keeps a crashed
// first deploy parallel: some directives may already be granted while the AC is still empty.
func isFirstDeployment(s plannerSnapshot) bool {
	total := 0
	for _, count := range s.AC.MemberCountsByIndex {
		total += count
	}
	return total == 0
}

func acCount(s plannerSnapshot, clusterName string) int {
	return s.AC.MemberCountsByIndex[allocatedIndex(s, clusterName)]
}

// behindSpec says the directive does not yet carry the current plan for its cluster; the count
// it should carry differs between first deploy (full spec count) and steady state (one step).
func behindSpec(s plannerSnapshot, t clusterTarget, wantCount int, allocations map[string]int) bool {
	d := s.Directives[t.ClusterName]
	if !d.Exists {
		return true
	}
	return d.Spec.MemberCount != wantCount ||
		d.Spec.TargetSpecHash != s.SpecHash ||
		d.Spec.LeadershipTerm != s.LeadershipTerm ||
		d.Spec.ProjectID != s.ProjectID ||
		d.Spec.ClusterIndex != allocations[t.ClusterName] ||
		!maps.Equal(d.Spec.IndexAllocations, allocations)
}

// memberCaughtUp says the member has SEEN the current instruction and its local CR copy matches
// the current spec — the fence discipline: a member behind either fence is never advanced past it.
func memberCaughtUp(s plannerSnapshot, clusterName string) bool {
	d := s.Directives[clusterName]
	return d.Exists && d.Status.ObservedGeneration == d.Generation && d.Status.ObservedSpecHash == s.SpecHash
}

func factsConverged(d directiveView) bool {
	return d.Status.StsApplied && d.Status.AgentRegistered && d.Status.InGoalState
}

// clusterInFlight reports a cluster mid-step: its granted count and AC count disagree (an AC
// write or a member shrink is pending), or it is granted members whose facts have not converged.
// An unreachable cluster's directive is invisible, but voting membership only changes through
// the AC — external and still readable — so the AC sitting at the spec count proves nothing is
// in flight there (steady-state cluster loss blocks nothing).
func clusterInFlight(s plannerSnapshot, clusterName string) bool {
	if d := s.Directives[clusterName]; d.Unreachable {
		return acCount(s, clusterName) != targetByName(s, clusterName).Members
	}
	g, ac := granted(s, clusterName), acCount(s, clusterName)
	if g != ac {
		return true
	}
	if g == 0 {
		return false // nothing ever moved on this cluster
	}
	d := s.Directives[clusterName]
	return !memberCaughtUp(s, clusterName) || !factsConverged(d)
}

// advancementCandidates returns every cluster allowed to move THIS pass — the exclusivity
// invariant (at most one cluster mid-change unless first deploy) lives here and nowhere else.
func advancementCandidates(s plannerSnapshot) []string {
	allocations := desiredAllocations(s)
	if isFirstDeployment(s) {
		// no convergence gate between first-deploy advancements; one write per pass over
		// consecutive passes still satisfies the rule
		var all []string
		for _, t := range targetsByAllocatedIndex(s, allocations) {
			if !s.Directives[t.ClusterName].Unreachable && behindSpec(s, t, t.Members, allocations) {
				all = append(all, t.ClusterName)
			}
		}
		return all
	}
	for _, t := range s.Targets {
		if clusterInFlight(s, t.ClusterName) {
			return nil // mid-change; its next step arrives via the other guards
		}
	}
	for _, t := range targetsByAllocatedIndex(s, allocations) {
		if s.Directives[t.ClusterName].Unreachable {
			continue // cannot instruct it; notProgressing reports it if the spec wants it moved
		}
		wantCount := scale.ReplicasThisReconciliation(scalerFor(s, t.ClusterName))
		if behindSpec(s, t, wantCount, allocations) {
			return []string{t.ClusterName}
		}
	}
	return nil
}

func scalerFor(s plannerSnapshot, clusterName string) interfaces.MultiClusterReplicaSetScaler {
	for _, scaler := range plannerScalers(s) {
		if scaler.MemberClusterName() == clusterName {
			return scaler
		}
	}
	return nil
}

// advancement writes the next directive step. Scale-up advances the grant first (the ladder:
// grant -> agentRegistered -> AC write -> inGoalState); scale-down advances the grant LAST —
// only after the AC already dropped the member and Ops Manager witnessed every remaining
// process back in goal state.
func advancement(s plannerSnapshot) (planDecision, bool) {
	allocations := desiredAllocations(s)

	if !s.AC.Read {
		return planDecision{}, false // cannot sequence without the AC; notProgressing reports it
	}

	// a mid-scale-down cluster's next step: the member shrink, gated on the OM witness. The AC
	// lagging the grant alone does not say down — a mid-scale-UP cluster looks the same between
	// its grant and its AC write — so the direction comes from the spec.
	if !isFirstDeployment(s) {
		for _, t := range targetsByAllocatedIndex(s, allocations) {
			g, ac := granted(s, t.ClusterName), acCount(s, t.ClusterName)
			if ac < g && t.Members < g {
				if !s.OMFacts.Read || !remainingProcessesConverged(s, allocations) {
					return planDecision{}, false // witness pending; notProgressing reports it
				}
				return planDecision{
					Kind:          decisionWriteDirective,
					TargetCluster: t.ClusterName,
					DirectiveSpec: desiredDirectiveSpec(s, t.ClusterName, ac, allocations),
					Reason:        fmt.Sprintf("shrinking cluster %s to %d members (automation config converged at the reduced membership)", t.ClusterName, ac),
				}, true
			}
		}
	}

	for _, clusterName := range advancementCandidates(s) {
		t := targetByName(s, clusterName)
		wantCount := t.Members
		if !isFirstDeployment(s) {
			wantCount = scale.ReplicasThisReconciliation(scalerFor(s, clusterName))
			if wantCount < granted(s, clusterName) {
				continue // a scale-down step is initiated by the AC write, never by the grant
			}
			if wantCount > granted(s, clusterName) && !memberCaughtUp(s, clusterName) {
				continue // fence discipline: never advance a member past what it has seen
			}
		}
		return planDecision{
			Kind:          decisionWriteDirective,
			TargetCluster: clusterName,
			DirectiveSpec: desiredDirectiveSpec(s, clusterName, wantCount, allocations),
			Reason:        fmt.Sprintf("advancing cluster %s to %d members", clusterName, wantCount),
		}, true
	}
	return planDecision{}, false
}

// remainingProcessesConverged is the scale-down witness: every hostname the automation config
// still expects — all clusters at their AC counts — is registered and in goal state per Ops
// Manager. The shrinking member's own facts are transiently false here and must not be consulted.
func remainingProcessesConverged(s plannerSnapshot, allocations map[string]int) bool {
	for _, t := range s.Targets {
		hostnames := dns.GetMultiClusterProcessHostnames(s.Name, s.Namespace, allocations[t.ClusterName], acCount(s, t.ClusterName), s.ClusterDomain, t.ExternalDomain)
		for _, hostname := range hostnames {
			state, ok := s.OMFacts.ProcessStates[hostname]
			if !ok || !state.Registered || !state.GoalAchieved {
				return false
			}
		}
	}
	return true
}

// orphanedACMembers holds every AC write while the automation config carries members at an
// index no spec cluster claims (a directive deleted AND its cluster removed from the spec —
// the removal guard needs a directive to see the removal, and every payload covers spec
// clusters only, so publishing would mass-drop the orphans in one write). Directive writes
// stay allowed — only the AC is fenced; an orphaned index also turns a would-be Noop into
// Pending, which is honest: this world is not converged.
func orphanedACMembers(s plannerSnapshot) (planDecision, bool) {
	if !s.AC.Read {
		return planDecision{}, false
	}
	claimed := make(map[int]struct{}, len(s.Targets))
	for _, t := range s.Targets {
		claimed[allocatedIndex(s, t.ClusterName)] = struct{}{}
	}
	indexes := make([]int, 0, len(s.AC.MemberCountsByIndex))
	for index := range s.AC.MemberCountsByIndex {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	for _, index := range indexes {
		if count := s.AC.MemberCountsByIndex[index]; count > 0 {
			if _, ok := claimed[index]; !ok {
				return planDecision{Kind: decisionNotProgressing, Reason: fmt.Sprintf("the automation config carries %d members at index %d that no spec cluster claims; refusing to publish membership that would drop them", count, index)}, true
			}
		}
	}
	return planDecision{}, false
}

// acWriteNeeded publishes membership. Scale-up (and first deploy): once every cluster granted
// beyond its AC count reports agentRegistered at the current spec, publish all granted counts.
// Scale-down: the AC write INITIATES each step — publish the first behind cluster at one less.
func acWriteNeeded(s plannerSnapshot) (acPayload, bool) {
	if !s.AC.Read {
		return acPayload{}, false
	}

	countsMatchAC := func(counts map[string]int) bool {
		for _, t := range s.Targets {
			if counts[t.ClusterName] != acCount(s, t.ClusterName) {
				return false
			}
		}
		return true
	}

	// scale-up / first deploy: some cluster granted beyond the AC. A mid-scale-down cluster also
	// has its grant above the (already reduced) AC — the shrinking pod keeps pinging Ops Manager,
	// so counting it here would re-publish the old membership and undo the reduction; the spec
	// direction excludes it.
	grantedAboveAC := false
	allRegistered := true
	grantedCounts := make(map[string]int, len(s.Targets))
	for _, t := range s.Targets {
		g := granted(s, t.ClusterName)
		grantedCounts[t.ClusterName] = g
		if g > acCount(s, t.ClusterName) && t.Members >= g {
			grantedAboveAC = true
			d := s.Directives[t.ClusterName]
			if !memberCaughtUp(s, t.ClusterName) || !d.Status.AgentRegistered {
				allRegistered = false
			}
		}
	}
	if grantedAboveAC {
		if !allRegistered || countsMatchAC(grantedCounts) {
			return acPayload{}, false
		}
		return acPayload{LeadershipTerm: s.LeadershipTerm, MemberCounts: grantedCounts}, true
	}

	// scale-down initiation: nothing in flight, some target below its granted (== AC) count
	for _, t := range s.Targets {
		if clusterInFlight(s, t.ClusterName) {
			return acPayload{}, false
		}
	}
	for _, t := range targetsByAllocatedIndex(s, desiredAllocations(s)) {
		if t.Members < granted(s, t.ClusterName) {
			counts := make(map[string]int, len(s.Targets))
			for _, other := range s.Targets {
				counts[other.ClusterName] = acCount(s, other.ClusterName)
			}
			counts[t.ClusterName] = acCount(s, t.ClusterName) - 1
			if countsMatchAC(counts) {
				return acPayload{}, false
			}
			return acPayload{LeadershipTerm: s.LeadershipTerm, MemberCounts: counts}, true
		}
	}

	// content refresh: a spec change that moves no member (say an additionalMongodConfig edit)
	// still changes the deployment content. Everything above is settled and every member is
	// converged at the current hash, so republish at the unchanged counts; the write re-stamps
	// the hash, making this fire exactly once per content change. Skipped while the AC is empty —
	// the first real membership write stamps it.
	if !isFirstDeployment(s) && s.AC.SpecHash != s.SpecHash {
		return acPayload{LeadershipTerm: s.LeadershipTerm, MemberCounts: grantedCounts}, true
	}
	return acPayload{}, false
}

// notProgressing is the terminal collector for every pass that converged on no action: it
// reports what the world is being waited on. The AdvancedAt age only flips the message from
// "waiting" to "not progressing" — it gates prose, never safety. A fully converged world falls
// through to Noop.
func notProgressing(s plannerSnapshot) (planDecision, bool) {
	if !s.AC.Read {
		return planDecision{Kind: decisionNotProgressing, Reason: "cannot read the automation config from Ops Manager"}, true
	}

	// spec skew: the leader plans from its local CR copy; a majority of members observing a
	// common different hash means the leader's own cluster has not received the latest spec
	if skewHash, ok := majoritySpecSkew(s); ok {
		return planDecision{Kind: decisionNotProgressing, Reason: fmt.Sprintf("spec skew: a majority of clusters observe spec hash %s while the leader's copy hashes to %s; check GitOps sync on the leader's cluster", skewHash, s.SpecHash)}, true
	}

	for _, t := range s.Targets {
		d := s.Directives[t.ClusterName]
		if !clusterInFlight(s, t.ClusterName) && granted(s, t.ClusterName) == t.Members && !behindSpec(s, t, t.Members, desiredAllocations(s)) {
			continue
		}
		if d.Unreachable {
			return planDecision{Kind: decisionNotProgressing, Reason: fmt.Sprintf("cluster %s is unreachable", t.ClusterName)}, true
		}
		reason := fmt.Sprintf("waiting for cluster %s: %s", t.ClusterName, classifyCluster(d, s.SpecHash).waitingOn())
		if d.Exists && s.Now.Sub(d.Spec.AdvancedAt.Time) > plannerNotProgressingAfter {
			reason = fmt.Sprintf("cluster %s has not progressed since %s: %s", t.ClusterName, d.Spec.AdvancedAt.Time.Format(time.RFC3339), classifyCluster(d, s.SpecHash).waitingOn())
		}
		return planDecision{Kind: decisionNotProgressing, Reason: reason}, true
	}
	return planDecision{}, false
}

func majoritySpecSkew(s plannerSnapshot) (string, bool) {
	observed := map[string]int{}
	for _, t := range s.Targets {
		if d := s.Directives[t.ClusterName]; d.Exists && d.Status.ObservedSpecHash != "" && d.Status.ObservedSpecHash != s.SpecHash {
			observed[d.Status.ObservedSpecHash]++
		}
	}
	for hash, count := range observed {
		if count > len(s.Targets)/2 {
			return hash, true
		}
	}
	return "", false
}

func targetByName(s plannerSnapshot, clusterName string) clusterTarget {
	for _, t := range s.Targets {
		if t.ClusterName == clusterName {
			return t
		}
	}
	return clusterTarget{ClusterName: clusterName}
}

func targetsByAllocatedIndex(s plannerSnapshot, allocations map[string]int) []clusterTarget {
	targets := append([]clusterTarget{}, s.Targets...)
	sort.SliceStable(targets, func(i, j int) bool {
		return allocations[targets[i].ClusterName] < allocations[targets[j].ClusterName]
	})
	return targets
}

func sortedClusterNames(directives map[string]directiveView) []string {
	names := make([]string, 0, len(directives))
	for name := range directives {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// clusterState is a log label, computed for logs, tests and support — never serialized: the
// directive status carries facts, and judgments stay planner-internal.
type clusterState int

const (
	awaitingDirective clusterState = iota
	awaitingDirectiveAck
	awaitingSpecSync
	applyingStatefulSet
	awaitingAgentRegistration
	awaitingGoalState
	inGoalState
)

func (c clusterState) String() string {
	return [...]string{"AwaitingDirective", "AwaitingDirectiveAck", "AwaitingSpecSync", "ApplyingStatefulSet", "AwaitingAgentRegistration", "AwaitingGoalState", "InGoalState"}[c]
}

func (c clusterState) waitingOn() string {
	switch c {
	case awaitingDirective:
		return "no directive written yet"
	case awaitingDirectiveAck:
		return "the member has not acknowledged the directive"
	case awaitingSpecSync:
		return "the member's local spec copy does not match yet (GitOps sync)"
	case applyingStatefulSet:
		return "the StatefulSet is not applied yet"
	case awaitingAgentRegistration:
		return "the automation agents have not registered with Ops Manager"
	case awaitingGoalState:
		return "the automation agents have not reached goal state"
	case inGoalState:
		// reachable while the cluster is still in flight: the directive looks converged but the
		// member's reported facts (applied hash/count) have not caught up yet
		return "the member's reported facts have not converged yet"
	}
	return "unknown"
}

func classifyCluster(d directiveView, specHash string) clusterState {
	switch {
	case !d.Exists:
		return awaitingDirective
	case d.Status.ObservedGeneration != d.Generation:
		return awaitingDirectiveAck
	case d.Status.ObservedSpecHash != specHash:
		return awaitingSpecSync
	case !d.Status.StsApplied:
		return applyingStatefulSet
	case !d.Status.AgentRegistered:
		return awaitingAgentRegistration
	case !d.Status.InGoalState:
		return awaitingGoalState
	}
	return inGoalState
}
