# ADR 0001: Immutable Build Artifacts and Zero-Rebuild CI/CD Pipeline

* **Status:** Accepted
* **Date:** 2026-08-05
* **Deciders:** Engineering / DevOps Team

---

## Context

In standard CI/CD setups, rebuilding source code or recalculating version tags at release time introduces non-determinism. Dependencies can drift, tests can flake during urgent deployments, and the code running in production might not strictly match the exact binary that passed testing.

To guarantee fast, predictable, and audit-safe deployments, we need clear, unbreakable invariants governing how code transitions from a Pull Request (PR) to production.

---

## Decision

We adopt a **Build-Once, Zero-Rebuild Release Pipeline** enforced by three core invariants and one explicit operational override.

### 1. Self-Contained PRs & Pre-Determined Versioning (PR Stage)
* Code submitted in a PR must be completely release-ready and up to date with the target branch.
* The PR must already know and encode its own release version. No version-bump commits or metadata fixes are permitted at or after release time.
* If a version or configuration fix is needed, it must happen within the PR *before* merging.

### 2. Immutable Promotion and Pinning (Merge Stage)
* Merging code triggers automated build and test suites.
* **Only commits and built artifacts (e.g., container images) that pass all required tests are marked as release-able.**
* Passing artifacts are explicitly promoted, pinned, and tagged in the registry. Non-promoted builds cannot be deployed to production.

### 3. Zero-Rebuild, Zero-Retest Releases (Release Stage)
* Releasing to production strictly means deploying an already-promoted artifact and tagging the corresponding passed commit.
* Releases **must never** re-compile code, re-build images, or re-run test suites. What was tested at merge time is *literally* what runs in production.

### Operational Caveat: Manual Promotion Override
* In rare scenarios where a test fails due to an external infrastructure issue, non-fixable dependency, or known non-blocking flake that does not impact release quality, authorized team members may manually promote a commit/artifact.
* Manual overrides must be explicitly logged with context explaining why the failing test was deemed non-impacting.

---

## Consequences

### Positive
* **100% Deterministic Deployments:** The exact bit-for-bit container/binary tested during CI is what lands in production.
* **Ultra-Fast Releases:** Deployments take seconds instead of minutes because compilation and test runs are already complete.
* **Clear AI Guardrails:** AI agents generating PRs or CI configurations have an unambiguous contract: *resolve versioning and testing left (at PR time), because post-merge environments are immutable.*

### Trade-offs & Mitigations

* **Registry Storage Footprint**
  * *Risk:* Pinning built artifacts for every passing merge could cause unbounded storage growth in the container registry.
  * *Mitigation:* Bounded by automated registry retention policies (e.g., FIFO cleanup, age expiration, or storage caps). Because releases strictly target recent or near-latest promoted commits, long-term retention of older intermediate images is unnecessary.

* **PR Discipline & Developer Friction**
  * *Risk:* Requiring code and versioning to be 100% release-ready at PR time shifts operational friction to developers.
  * *Mitigation:* Heavy use of pre-commit hooks, local validation, and automated CI fixers. CI checks block any PR merging if formatting or versioning rules are violated, removing manual guesswork for the author.

