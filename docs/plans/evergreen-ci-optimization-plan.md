# Evergreen CI/CD Optimization Plan

**Created:** 2026-01-27  
**Epic:** `mongodb-kubernetes-ufj`  
**Status:** Planning Complete

## Executive Summary

This plan outlines 9 approved improvements to the Evergreen CI/CD pipeline, prioritized based on the current state of master (flaky, 2-3 tests always failing).

**Expected Impact:**
- 30-50% reduction in PR build time
- Improved developer experience
- Reduced compute costs (~$500-1000/month)
- Stable, green master branch

---

## Implementation Phases

### Phase 1: Stabilize First (Address Flakiness)

> ⚠️ **Critical:** Master is currently flaky with 2-3 tests always failing. This must be addressed before enabling stepback or other optimizations that assume a stable baseline.

| Order | Issue ID | Title | Effort | Status |
|-------|----------|-------|--------|--------|
| 1 | `mongodb-kubernetes-pez` | Test stability improvements | Large (60-100h) | ○ Open |
| 2 | `mongodb-kubernetes-xkf` | Add stepback: true | Very Low | ○ Open (blocked) |

**Issue `pez` includes:**
1. Flaky test detection and quarantine system
2. Consolidate redundant TLS test variants (6+ → 3)
3. Consolidate SCRAM test variants
4. Test coverage analysis

**Why stepback is blocked:** Enabling stepback on a flaky master will trigger on every commit, running 5-10 previous commits chasing false positives. Wait until master is stable.

---

### Phase 2: Quick Wins (Low Effort, Immediate Value)

| Order | Issue ID | Title | Effort | Impact |
|-------|----------|-------|--------|--------|
| 3 | `mongodb-kubernetes-11w` | Move OM70 variants to master-only | 5 min | 2-3h/PR saved |
| 4 | `mongodb-kubernetes-fds` | Race detector to master + Helm caching | 1-2h | 1-1.5h/PR saved |
| 5 | `mongodb-kubernetes-v7p` | Remove unused test tasks | 1-2h | Cleaner config |
| 6 | `mongodb-kubernetes-0ks` | Skip E2E for non-code PRs | 30 min | 90% time saved on docs PRs |

**Issue `fds` scope (reduced):**
- ✅ Move race detector variant to master-only
- ✅ Implement Helm chart caching
- ✖️ Skip: OLM tests (keep on PRs)
- ✖️ Skip: Retry policies (already implemented)
- ✖️ Skip: YAML anchor consolidation

**Issue `0ks` implementation (revised):**
- **Approach:** Use project-level `ignore:` block (NOT per-variant `paths:`)
- **Effort:** 30 minutes (down from 2-3h estimate)
- **How it works:** Gitignore-style patterns at top of `.evergreen.yml`
  - If PR only changes ignored files → Evergreen sends successful status, no tests run
  - If PR changes any non-ignored file → full test suite runs

```yaml
# Add near top of .evergreen.yml
ignore:
  - "*.md"
  - "docs/**"
  - "README*"
  - "CHANGELOG*"
  - "LICENSE"
  - ".github/**"
  - "scripts/dev/**"
```

- Supports `!` for exceptions (e.g., `!scripts/dev/important.sh`)
- No need to modify 25+ variants individually
- Unit tests, linting still run via normal triggers

---

### Phase 3: Infrastructure Improvements

| Order | Issue ID | Title | Effort | Impact |
|-------|----------|-------|--------|--------|
| 7 | `mongodb-kubernetes-fz0` | Update to Ubuntu 24.04 | 1h | Security, EOL compliance |
| 8 | `mongodb-kubernetes-03g` | Add task-specific timeouts | 4-8h | Faster failure detection |

**Ubuntu 24.04 updates:**
- `e2e_om70_kind_ubi`: ubuntu2204 → ubuntu2404
- `e2e_om80_kind_ubi`: ubuntu2204 → ubuntu2404
- `e2e_operator_perf*`: ubuntu1804 → ubuntu2404 (EOL!)
- `unit_tests`: ubuntu2204 → ubuntu2404

**Timeout recommendations (from API analysis):**
| Category | Max Observed | Suggested Timeout |
|----------|-------------|-------------------|
| Longest tasks (OM HTTPS, TLS sharded) | 75-95 min | 120 min |
| OM Backup/Restore | 40-60 min | 90 min |
| Multi-cluster | 40-67 min | 90 min |
| Standard E2E | 20-40 min | 60 min |
| Unit tests | ~5 min | 15 min |

---

### Phase 4: Larger Refactoring

| Order | Issue ID | Title | Effort | Impact |
|-------|----------|-------|--------|--------|
| 9 | `mongodb-kubernetes-2d7` | Consolidate static/non-static with shrub | 2-3 days | 30% config reduction |

**Approach:**
- Create `scripts/evergreen/generate_e2e_variants.py` using shrub
- Define base variants once, generate static/non-static pairs
- Output to `.evergreen-generated.yml` (included via `include:`)
- Remove duplicated YAML from main config

---

## Closed/Deferred Issues

| Issue ID | Title | Decision | Reason |
|----------|-------|----------|--------|
| `mongodb-kubernetes-9ju` | Split large task groups | ✖️ Closed | Already parallelized with `max_hosts: -1` |
| `mongodb-kubernetes-gpy` | Move multi-cluster to master-only | ⏸️ Deferred | Too important to skip on PRs |
| `mongodb-kubernetes-81g` | Changed-file-based test selection | ✖️ Closed | Too complex, shared codebase |
| `mongodb-kubernetes-u4v` | Move backup/KMIP to master-only | ✖️ Closed | Keep on PRs |
| `mongodb-kubernetes-6w4` | Clean up IBM Z variants | ✖️ Closed | Leave as-is |

---

## Next Steps

1. **Immediate:** Start Phase 1 - implement flaky test tracking
2. **This sprint:** Complete Phase 2 quick wins
3. **Next sprint:** Phase 3 infrastructure + Phase 4 refactoring
4. **After stable:** Enable stepback (`xkf`)

---

## References

- Epic: `bd show mongodb-kubernetes-ufj`
- Evergreen config: `.evergreen.yml`, `.evergreen-tasks.yml`, `.evergreen-functions.yml`
- Shrub examples: `scripts/evergreen/e2e/mco/create_mco_tests.py`

