package operator

/*
Pseudo code for now. + maybe find a better name for the file

This controller watches:
- Directive CRD of all member cluster, to observe their status (poll vs informer TBD)
- The lease CRDs of all member clusters (poll vs informer TBD)
  Note (2026-08-19): checked kubernetes-sigs/multicluster-runtime -> not for us. It targets the hub topology
  (one manager reconciling many clusters, dynamic discovery via providers); we go the opposite way.
  If we want foreign informers later: main.go:246 already does cluster.New per member + mgr.Add (vanilla
  controller-runtime), WatchesRawSource on those caches gives cross-cluster watches with zero new deps.
  Lease reads/writes must bypass any cache regardless (CAS on a stale read is wrong by construction).
- The MDBMulticluster CRD, to trigger new changes observed

It writes to:
- Directive Spec of all clusters
- the automation config

Every write carries extra fields for the consumer to check:
- directive spec: leadership term + targetSpecHash (the member fences on both)
- AC: embed the leadership term in the payload -> audit trail + term floor for the majority-loss DR runbook.
  Where to store it, verified (2026-08-19): namespaced key inside the AC top-level "options" object -> schemaless map, round-trips PUT->store->GET. A new top-level field is REJECTED (strict Jackson). TBD: confirm the agent ignores the unknown options key.
  TODO 4 RESOLVED 2026-08-19: OM public API has NO client CAS. Payload version is ignored (validation commented out), server rebuilds from its own in-request read, stale writer wins ("later modification wins"). Internal 409 = same-instant races only.
  => the hold-off IS the only AC protection. API-side locking = CLOUDP-373090 (OM team, ride it).

Has the kubernetes permissions for foreign cluster for:
- Writing directive specs (in same namespace, TBD if we support cross namespace)
- Reading directive status
- Read/write the lease object



Role:
Any action is decided by collecting the state of the world, computing the current step with plan. Then it acts (state machine). one action is performed, then we return from the reconcile loop (with eventually a delay)

What goes in snapshot (feeds the plan function):
- Time
- Lease term
- Desired MDBMulti Spec + hash
- All directives (even removed clusters' ones)
- Current AC

- Validate the user written MDBMulti Spec
- Prepare OM connection
- publish OM config updates
- give directives to member clusters for making progress on kube resources updates:
  - compute next scaling (use the scaler object) and pass it via directive
  -
- compute automation config state, push it (must be the only writer) at the right time. based on its state machine. Re-uses "publishAutomationConfigFirst" boolean, to decide if it's before or after STS modifications in kube.
- set historical and current cluster indexes (in the directive), majority write

Checks:
- Ensuring it is the leader before anything
- Takeover hold-off: after winning an election, wait one lease DURATION (wall clock, ~10s) before the first guarded write.
  Not "a term": term = version number of leadership (44th, 45th president), duration = timeout (the parking meter). The wait is sized so a zombie's in-flight write lands before our first fresh read.

Leader election / renewal machinery: full protocol in .spike/poc/leader-election-protocol.md (2026-08-19).
Shape: elector as a separate manager.Runnable (own ticker — heartbeats never queue behind a slow OM PUT).
Talks to this controller via: elector.Current() (term, isLeader) read once at snapshot time (a term, never a bare bool),
GenericEvent into a source.Channel to wake us on transitions, optional ctx cancel on loss.
Renewal = re-prove majority every ~10s; on failure: stop guarded work, then step down.
Seam in code (2026-08-20): the Elector interface in mdbmulti_decentralized_elector.go; StaticElector
("am I the designated leader cluster?") stands in until the majority lease lands (roadmap M3.7).

*/
