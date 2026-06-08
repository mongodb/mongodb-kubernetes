# KUBE-38: Search TLS-enable availability e2e

**Goal:** Characterise and regression-guard the availability of MongoDB `$search` when search TLS is
enabled on a *live* deployment (off→on), plus a subsequent cert rotation, under sustained query load.

**Stack:** base PR `search/ga-base-KUBE-38-tls-bootstrap-harness` (non-TLS bootstrap harness + smoke) ←
test PR `search/ga-base-KUBE-38-tls-enable-availability` (the enable + rotation suite). Both off
KUBE-40 (`search/ga-base-KUBE-40-upgrade-availability`).

See: [docs/plans/2026-06-08-kube-38-tls-enable-availability.md](../plans/2026-06-08-kube-38-tls-enable-availability.md)

## Summary

The search availability suites (`tests/search/`) all bootstrap a TLS-on deployment. This work adds the
inverse — a **non-TLS** search bootstrap — and a suite that **enables** search TLS on it under load.

Two TLS layers exist and must not be conflated:
- **mongod's own client TLS** (`mdb.spec.security.tls`) — for client→mongod. Always on; the query path
  and the availability tester use it. Not toggled here.
- **search TLS** — the mongod↔mongot gRPC channel, governed by the MongoDBSearch CR
  (`spec.security.tls`) *and* the source mongod's `searchTLSMode` setParameter. This is what off→on
  toggles.

## Key finding (live probe)

Enabling search TLS on a running deployment is an **inherent bounded outage, not a ride-through**:
- A non-TLS search deployment serves `$search` fine (mongot `ConfigTLSModeDisabled` gRPC, mongod
  `searchTLSMode=disabled`).
- The straight flip (CR `spec.security.tls` + mongod `requireTLS`) produced **~108s** of continuous
  outage, recovering well after the pods were Ready — the source-mongod automation agent reconverging
  `searchTLSMode` dominates the tail.
- An intermediate `preferTLS` step does **not** ride through (~70s outage): `searchTLSMode` gates
  whether mongod uses TLS *at all* on the outbound channel — `preferTLS`/`allowTLS` make it *attempt*
  TLS, so it broke against the still-plaintext mongot the moment it applied, before CR TLS was enabled.
- mongot's gRPC TLS mode is binary (`Disabled`↔`TLS`, no dual-listen), and no `searchTLSMode` tolerates
  both endpoint kinds at once. **Therefore no zero-downtime enable path exists with current semantics.**

The suite asserts the deployment rolls both groups onto TLS, the mongod reaches `requireTLS`, and search
**recovers** (the recovery wait bounds the outage) — it does **not** assert no-outage.

## Architecture

- **Harness (base PR):** a `search_tls: bool = True` knob on `SearchDeploymentConfig`, threaded
  additively through the shared bootstrap mixins. `search_tls=False` → source mongod
  `searchTLSMode=disabled`, MongoDBSearch CR omits `spec.security.tls` (via
  `mdbs_for_ext_rs_source(set_search_tls=False)`), and the LB/search cert steps are skipped. Default
  `True` leaves every existing TLS suite untouched. A `search_set_parameters(tls_mode=...)` helper
  parametrises the previously-hardwired `searchTLSMode`. Proven by `search_availability_nontls_smoke.py`.
- **Suite (test PR):** `search_availability_tls_enable.py` bootstraps non-TLS, then:
  1. issues LB + search certs,
  2. enables TLS under load (CR `spec.security.tls` + mongod `searchTLSMode=requireTLS`), waits both
     groups replaced + Running + search recovered, asserts rolls + `requireTLS` + post-recovery
     progress + a clean steady-state window,
  3. rotates the now-live cert (`certsSecretPrefix`→`certs-rot`) — a ride-through asserted with the
     shared `assert_rolled_through` bound.

## Security considerations

No new credentials or PII. The suite exercises TLS material handling (cert issuance + rotation) only;
all certs are cert-manager-issued in the test namespace.

## Decisions log

- **Dropped the config-change (resourceRequirements) suite** — its "mutate a CR field → group rolls →
  ride-through" shape duplicates the rolling-restart suite; the off→on enable is the qualitatively
  novel coverage. (User call; agreed after assessment.)
- **Folded TLS cert rotation into the enable suite** — both are TLS day-2 ops on one deployment; the
  enable leaves it TLS-on so rotation runs next naturally. The standalone rotation suite is retired.
- **Pivoted from "graceful preferTLS enable" to "bounded-outage enable"** — the probe disproved the
  preferTLS ride-through hypothesis (see Key finding).
- **Assert recovery, not a disruption bound** — the enable outage tail (~108s) and the
  `max_consecutive_failure` approximation are too noisy for a hard bound; the steady-state recovery
  wait is the bound. Rotation keeps the `assert_rolled_through` bound (it's a genuine ride-through).

## Follow-up (product gap)

There is no zero-downtime way to enable search TLS on a live deployment. mongot gRPC dual-listen
(serve plaintext + TLS during a roll), or a tolerant mongod outbound search mode, would enable it.
Worth raising to the search team.
