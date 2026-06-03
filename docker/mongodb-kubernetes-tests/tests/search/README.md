# Search availability e2e tests

These suites assert that MongoDB `$search` stays available across data-plane and
infrastructure disruptions, under sustained query load. Each test runs a
`SearchAvailabilityBackgroundTester` (from `tests/common/search/`) over the disruption window
and applies a verdict per **query type**:

- **New query** (`oneshot` mode) — a fresh `$search` each tick. Needs a live mongot + envoy, so
  it briefly blips on a disruption and recovers once a surviving replica/endpoint serves.
- **Open cursor** (`paging` mode) — pages a long-lived cursor. mongod materializes the cursor and
  eagerly drains mongot's stream into a buffer, so an open cursor **often rides through** a mongot
  disruption (the buffer covers the gap) — but a `getMore` landing mid-cycle can still drop it once
  before it reopens, so ride-through is timing-dependent, not guaranteed. The drained sub-check
  force-drains past the buffer to surface the fault deterministically. Envoy pins each mongod→mongot
  HTTP/2 stream to one upstream connection, so cycling the serving envoy pod RSTs the stream.

For disruptions that can drop an open cursor, the test pairs a background-window check (new-query
availability + cursor ride-through where expected) with a `paging_baseline_and_fault` **drained
sub-check** that force-drains past the buffer to prove the cursor fault is observable.

## Suites

| File | Marker | Scenarios |
|---|---|---|
| `search_availability_smoke.py` | `e2e_search_availability_smoke` | steady-state baseline (no disruption) |
| `search_connectivity_tool.py` | `e2e_search_connectivity_tool` | mongot pod kill, envoy pod kill, mongot scale up/down/to-zero |
| `search_availability_rolling_restart.py` | `e2e_search_availability_rolling_restart` | envoy Deployment roll, mongot StatefulSet roll |
| `search_availability_envoy_scale.py` | `e2e_search_availability_envoy_scale` | envoy scale up (additive) then down to 1, via `spec.loadBalancer.managed.replicas` |
| `search_availability_infra.py` | `e2e_search_availability_infra` | node drain (cordon + evict), operator restart |

Each file deploys once (external-source RS + managed Envoy LB, multi-replica defaults) via the
shared bootstrap test-class chain, then runs its scenarios with a steady-state gate between them.

## Verdict matrix

| Scenario | New query | Cursor (bg ride-through) | Cursor (drained sub-check) |
|---|---|---|---|
| envoy roll | blip → recover | ride-through or transient drop → reopen | `cursor_lost` observable |
| mongot roll | blip → recover | ride-through or transient drop → reopen | `cursor_lost` observable |
| envoy scale up | no outage | rides through (no outage) | n/a (additive) |
| envoy scale down (→1) | recover (log-only) | may drop → log-only + recover | n/a (see envoy roll) |
| node drain | outage → recover on uncordon | outage → recover | n/a |
| operator restart | continuous | continuous (control plane off the data path) | n/a |

Assertions check availability *properties*, never exact operation counts. The roll cursor cells
assert the open cursor serves *fresh* pages after recovery (succeeded grows past a post-recovery
snapshot), tolerating a transient drop+reopen; the deterministic cursor fault is hard-asserted as
`cursor_lost` by the drained sub-checks. The envoy scale-down cursor cell is log-only because a
2→1 removal drops a pinned cursor only ~50/50 — its deterministic coverage lives in the roll
drained sub-checks. Every scenario closes with a steady-state gate proving full recovery.

## Notes

- Node drain is modelled as cordon + evict on the single-node kind env (a true drain is
  meaningless with nowhere to reschedule): pods go Pending → new queries fault → uncordon →
  recovery.
- Operator restart skips locally, where the operator runs out-of-cluster (`replicas=0`) and there
  is no operator pod to delete. It is exercised in CI.
