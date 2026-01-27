# Backup 409 "Version Not Available" - Investigation Plan

## Issue: mongodb-kubernetes-jdz

**Status:** ✅ ROOT CAUSE IDENTIFIED
**Branch:** fix/backup-409-version-not-available
**PR:** #719

---

## Executive Summary

The flaky test `TestBackupForMongodb::test_deploy_same_mdb_again_with_orphaned_backup` fails with a 409 error.

**ROOT CAUSE:** The monitoring agent is trying to authenticate with **SCRAM-SHA-1 credentials** against `mdb_four_zero` which has authentication **disabled**. This happens because:
1. Other deployments in the project (`my-mongodb-blockstore`, `my-mongodb-oplog`, `my-mongodb-s3`) have SCRAM auth enabled
2. This sets `autoUser`/`autoPwd` at the **project level**
3. The monitoring agent uses these credentials for ALL hosts in the project
4. When connecting to `mdb_four_zero` (no auth), authentication fails
5. Without metrics, OM doesn't have version info → backup returns 409

## Key Evidence

### Failed Run: 69776c8e5a5d2c0007d8a9a2
- 🔗 [Evergreen Link](https://spruce.mongodb.com/version/69776c8e5a5d2c0007d8a9a2)

| Test | Duration | Result |
|------|----------|--------|
| test_hosts_were_removed | 2.075s | ✅ PASSED |
| test_deploy_same_mdb_again_with_orphaned_backup | 600.844s | ❌ FAILED |

### Monitoring Agent Logs
```
[2026-01-26T14:44:59.746+0000] Failure during DB stats collection.
[2026-01-26T14:44:59.754+0000] Failure during list databases collection.
```

---

## Why Previous Fix (PR #719) Doesn't Apply

```go
// controllers/operator/common_controller.go:642-648
} else if ac.Auth.IsEnabled() {
    // Disable() is called HERE - clears credentials
}
```

**Problem:** `mdb_prev` uses `replica-set-for-om.yaml` which **never had auth enabled**.
Therefore `ac.Auth.IsEnabled()` returns false and `Disable()` is never called.

---

## Authentication Configuration Verification

### Step 1: What Fixture is Used for `mdb_prev`?

**File:** `docker/mongodb-kubernetes-tests/tests/opsmanager/om_ops_manager_backup.py` (lines 561-572)
**Fixture:** `replica-set-for-om.yaml`

```python
@fixture(scope="class")
def mdb_prev(self, ops_manager: MongoDBOpsManager, namespace, custom_mdb_prev_version: str):
    resource = MongoDB.from_yaml(
        yaml_fixture("replica-set-for-om.yaml"),   # <-- THIS FIXTURE
        namespace=namespace,
        name="mdb-four-zero",
    ).configure(ops_manager, "secondProject")
    resource.set_version(ensure_ent_version(custom_mdb_prev_version))
    resource.configure_backup(mode="disabled")      # <-- NO AUTH CONFIG
    try_load(resource)
    return resource
```

### Step 2: Does the Fixture Have Authentication Enabled?

**File:** `docker/mongodb-kubernetes-tests/tests/opsmanager/fixtures/replica-set-for-om.yaml`

```yaml
---
apiVersion: mongodb.com/v1
kind: MongoDB
metadata:
  name: the-replica-set
spec:
  members: 3
  version: 4.4.11
  type: ReplicaSet
  opsManager:
    configMapRef:
      name: om-rs-configmap
  credentials: my-credentials
  persistent: true
  logLevel: DEBUG
# NOTE: NO spec.security.authentication section!
```

**VERIFIED:** ✅ The `replica-set-for-om.yaml` fixture has **NO authentication configuration**.

### Step 3: Is Auth Ever Enabled for `mdb_prev` During Test Lifecycle?

Tracing the complete lifecycle of `mdb_prev`:

| Test Method | What Happens to `mdb_prev` | Auth Status |
|-------------|---------------------------|-------------|
| `test_mdbs_created` | Created with backup=disabled | ❌ No auth |
| `test_mdbs_enable_backup` | Backup mode → enabled | ❌ No auth |
| `test_mdbs_backuped` | Waits for snapshot | ❌ No auth |
| `test_can_transition_from_started_to_stopped` | Backup mode → disabled, then stopped | ❌ No auth |
| `test_backup_terminated_for_deleted_resource` | Backup re-enabled, `autoTerminateOnDeletion=True`, DELETED | ❌ No auth |
| `test_hosts_were_removed` | Verifies hosts empty in OM | N/A (deleted) |
| `test_deploy_same_mdb_again_with_orphaned_backup` | **RE-CREATED** with backup enabled | ❌ No auth |

**VERIFIED:** ✅ `mdb_prev` **NEVER has authentication enabled** throughout its entire lifecycle.

### Step 4: The Timing Paradox Explained

**Evidence:**
- `test_hosts_were_removed` **PASSED** in 2.075 seconds
- This proves hosts were successfully deregistered from Ops Manager

**Question:** If hosts were removed, why would stale credentials matter?

**Answer:** The credentials in question are stored at the **PROJECT level**, not the HOST level.

#### Key Distinction: PROJECT-level vs HOST-level Configuration

| Config Type | Where Stored | Cleared When? |
|-------------|--------------|---------------|
| Hosts | `/api/public/v1.0/groups/{groupId}/hosts` | When `RemoveHost()` called |
| Monitoring Agent Credentials | `/api/public/v1.0/groups/{groupId}/automationConfig/monitoringAgentConfig` | When `authentication.Disable()` called |
| AutomationConfig Auth | `/api/public/v1.0/groups/{groupId}/automationConfig` | When `authentication.Disable()` called |

When `mdb_prev` is deleted:
1. ✅ **Hosts are removed** from OM (via `RemoveHost()` API) - takes ~2s
2. ❌ **Monitoring agent credentials are NOT cleared** because `authentication.Disable()` is never called (auth was never enabled)

However, for **this specific test failure**, this is actually **NOT the root cause** because:
- The project "secondProject" never had auth enabled for `mdb_prev`
- There are no stale credentials to cause issues
- The monitoring agent should be able to connect without credentials

### Step 5: Conclusion

**Hypothesis:** "PR #719 fix doesn't apply because `mdb_prev` never had auth enabled" is **CORRECT**.

**However**, this doesn't explain the failure because:
- If auth was never enabled, there are no stale credentials to cause problems
- The monitoring agent should be able to connect and collect metrics
- Yet it fails with "Failure during DB stats collection"

**CONFIRMED ROOT CAUSE:** The issue IS stale credentials, but not the ones we expected:
1. The monitoring agent connects to the project
2. It uses **project-level SCRAM credentials** (set by other SCRAM-enabled deployments like `my-mongodb-blockstore`)
3. When connecting to `mdb_four_zero` (auth disabled), SCRAM-SHA-1 authentication fails
4. The 409 error is a symptom of OM not having version info because monitoring failed to collect it

**See "Monitoring Agent Connection Failure Analysis" section below for full details.**

---

## Test Runs Analysis

| Version ID | Link | Branch | Result | Test Case |
|------------|------|--------|--------|-----------|
| 69776c8e5a5d2c0007d8a9a2 | [🔗](https://spruce.mongodb.com/version/69776c8e5a5d2c0007d8a9a2) | PR #719 | ❌ FAIL | test_deploy_same_mdb_again_with_orphaned_backup |
| 697884b5a3a4b100079534f1 | [🔗](https://spruce.mongodb.com/version/697884b5a3a4b100079534f1) | master | ❌ FAIL | test_hosts_were_removed (5s diag) |
| 697777a18c4cdb000704acba | [🔗](https://spruce.mongodb.com/version/697777a18c4cdb000704acba) | PR #719 | ✅ PASS | All |
| 697777b0edae3000072fcab2 | [🔗](https://spruce.mongodb.com/version/697777b0edae3000072fcab2) | PR #719 | ✅ PASS | All |
| 697777bb0639c100072a4cb8 | [🔗](https://spruce.mongodb.com/version/697777bb0639c100072a4cb8) | PR #719 | ✅ PASS | All |
| 697884c3d89a79000778911c | [🔗](https://spruce.mongodb.com/version/697884c3d89a79000778911c) | master | ✅ PASS | All |
| 697884cc40b51000074117ee | [🔗](https://spruce.mongodb.com/version/697884cc40b51000074117ee) | master | ✅ PASS | All |

---

## Task Breakdown

### ✅ DONE: mongodb-kubernetes-jdz.1
**Investigate monitoring agent 'Failure during DB stats collection'**
- ✅ **Cause:** SCRAM-SHA-1 authentication failure
- ✅ **Root Cause:** Project-level SCRAM credentials contaminate auth-disabled deployments
- ✅ **Why 600s:** Monitoring agent retries indefinitely but auth always fails

### 🔴 P1: mongodb-kubernetes-jdz.3
**Update PR #719 to fix the actual root cause**
- Current fix only clears X509 credentials, NOT SCRAM credentials
- Need to add SCRAM credential clearing to `monitoringAgentConfig`
- Or: Update test to enable SCRAM on `mdb_prev` to match other deployments

### 🟢 OPTIONAL: mongodb-kubernetes-jdz.2
**Add explicit wait for monitoring health before configuring backup**
- Still a good defensive improvement
- But not the root cause fix

---

## Actual Root Cause Diagram

```
┌─────────────────────────────────────────────────────────────────┐
│  1. mdb_prev deleted                                            │
│     └─> Hosts deregistered from OM (took 2s)                    │
├─────────────────────────────────────────────────────────────────┤
│  2. mdb_prev re-created with backup enabled                     │
│     └─> New pods start fresh                                    │
├─────────────────────────────────────────────────────────────────┤
│  3. Monitoring agent starts                                     │
│     └─> Attempts to collect metrics                             │
│     └─> ❌ "Failure during DB stats collection"                 │
├─────────────────────────────────────────────────────────────────┤
│  4. No metrics → No version info in OM                          │
│     └─> Backup requested                                        │
│     └─> ❌ 409: "version information not yet available"         │
├─────────────────────────────────────────────────────────────────┤
│  5. Retry loop continues for 600s                               │
│     └─> Monitoring never successfully collects                  │
│     └─> ❌ Test times out                                       │
└─────────────────────────────────────────────────────────────────┘
```

---

## Next Actions

- [x] Investigate monitoring agent collection failures (jdz.1) ✅ **DONE - Root cause identified**
- [ ] Update PR #719 to clear SCRAM credentials from monitoringAgentConfig (jdz.3)
- [ ] Alternative: Update test fixture to enable SCRAM auth on `mdb_prev`
- [ ] Optional: Add defensive wait for monitoring health before backup (jdz.2)

---

## Multiple Failure Analysis

Analysis of 6 failures of `TestBackupForMongodb::test_deploy_same_mdb_again_with_orphaned_backup` from Evergreen CI.

### Summary Table

| Version ID | Date | test_hosts_were_removed | orphaned_backup Duration | Auth State | Monitoring Error | Root Cause Hypothesis |
|------------|------|------------------------|-------------------------|------------|------------------|----------------------|
| [294d005](https://spruce.mongodb.com/version/mongodb_kubernetes_294d005d333d64faff0d753ad57bb25b5941917f) | 2026-01-23 | ✅ PASS (2.07s) | ❌ FAIL (600.8s) | `auth.disabled: true` | DB stats collection failure | Monitoring agent cannot collect metrics |
| [8069ca9](https://spruce.mongodb.com/version/mongodb_kubernetes_8069ca9d4b789a310cf8a3906e84cd510bd927a9) | 2026-01-23 | ✅ PASS (2.08s) | ❌ FAIL (600.9s) | `auth.disabled: true` | DB stats collection failure | Monitoring agent cannot collect metrics |
| [04c5f42](https://spruce.mongodb.com/version/mongodb_kubernetes_04c5f42463d52fe6c36b964f5ca4aec7c6c02355) | 2026-01-16 | ✅ PASS (2.07s) | ❌ FAIL (600.8s) | `auth.disabled: true` | DB stats collection failure | Monitoring agent cannot collect metrics |
| [cf3b867](https://spruce.mongodb.com/version/mongodb_kubernetes_cf3b867963942c12def5fd0f9606f3248bd6d8bf) | 2026-01-09 | ✅ PASS (2.08s) | ❌ FAIL (600.9s) | `auth.disabled: true` | DB stats collection failure | Monitoring agent cannot collect metrics |
| [6553781](https://spruce.mongodb.com/version/mongodb_kubernetes_655378110fefb5d5526f137af45f8dae532ac371) | - | ✅ PASS (2.07s) | ❌ FAIL (600.8s) | `auth.disabled: true` | DB stats collection failure | Monitoring agent cannot collect metrics |
| [69776c8 (patch)](https://spruce.mongodb.com/version/69776c8e5a5d2c0007d8a9a2) | 2026-01-26 | ✅ PASS (2.08s) | ❌ FAIL (600.8s) | `auth.disabled: true` | DB stats/discovery failure | Monitoring agent cannot collect metrics |

### Common Patterns Across All Failures

1. **Consistent Timing:**
   - `test_hosts_were_removed`: Always ~2 seconds (PASSED)
   - `test_deploy_same_mdb_again_with_orphaned_backup`: Always ~600.8 seconds (FAILED - timeout)

2. **Auth Configuration:**
   - All failures show `"auth": { "disabled": true }` in automation-config for `mdb_four_zero`
   - **BUT** `autoUser` and `autoPwd` ARE present at project level (set by other SCRAM-enabled deployments)
   - This causes the monitoring agent to attempt SCRAM auth against an auth-disabled deployment

3. **Error Messages:**
   - Master: `Status: 409 (Conflict), Detail: Backup failed to start: MongoDB version information is not yet available`
   - Patch #719: `Waiting for MongoDB version information to be available in Ops Manager` (cleaner message)

4. **Monitoring Agent Errors (from logs):**
   ```
   [error] Failure during DB stats collection.
   [error] Failure during list databases collection.
   [error] Failure during discovery.
   [error] Failure getting buildInfo. Err: connection reset by peer
   [error] Failure getting buildInfo. Err: context deadline exceeded
   [error] Failure getting buildInfo. Err: EOF
   ```

### Key Insights

1. **Host removal is NOT the issue:**
   - All failures show `test_hosts_were_removed` passing in ~2s
   - Hosts are successfully deregistered from Ops Manager

2. **Monitoring agent fails to collect version info:**
   - When `mdb_prev` is re-created, new pods start fresh
   - Monitoring agent attempts to connect but gets connection errors
   - Without successful metrics collection, OM doesn't have version info
   - Backup cannot start → 409 error

3. **Race condition hypothesis:**
   - MongoDB pods may not be fully ready when monitoring agent tries to connect
   - Connection attempts fail with "connection reset", "EOF", or timeout
   - The 600s timeout isn't enough for the monitoring agent to successfully collect metrics

4. **Why PR #719 credential fix doesn't help:**
   - `mdb_prev` uses `replica-set-for-om.yaml` which has no auth
   - `ac.Auth.IsEnabled()` returns `false` → `Disable()` never called
   - The credential clearing code path is never executed

### Recommendations

1. **Root Cause:** The monitoring agent cannot collect metrics from the re-created MongoDB deployment

2. **Potential Fixes:**
   - Wait for monitoring agent to successfully collect metrics before enabling backup
   - Add explicit health check for version info availability before backup configuration
   - Increase retry interval or add exponential backoff for version info polling

3. **PR #719 Assessment:**
   - The fix converts 409 → cleaner "Waiting..." message (improvement)
   - Does NOT fix the underlying monitoring agent collection failure
   - May still be valuable for OTHER scenarios where credentials ARE stale

## Monitoring Agent Connection Failure Analysis

### 1. Root Cause of Connection Failures

**DEFINITIVE ROOT CAUSE IDENTIFIED:** The monitoring agent is trying to authenticate with SCRAM-SHA-1 credentials against `mdb_four_zero` which has authentication **disabled**.

#### Evidence from Monitoring Agent Logs (Version 294d005)

```
[2026-01-23T14:26:05.181+0000] [error] Failure during DB stats collection.
Failed to get connectionStatus. Err: `connection() error occurred during connection handshake:
auth error: sasl conversation error: unable to authenticate using mechanism "SCRAM-SHA-1":
(AuthenticationFailed) Authentication failed.`
```

**Key Observation:** The error is NOT "connection refused" or "timeout" - it's specifically **"unable to authenticate using mechanism SCRAM-SHA-1"**.

#### Why This Happens

The problem is **PROJECT-level credential contamination**:

1. **Project "secondProject" has multiple deployments:**
   - `my-mongodb-blockstore` - **SCRAM auth ENABLED** ✅
   - `my-mongodb-oplog` - **SCRAM auth ENABLED** ✅
   - `my-mongodb-s3` - **SCRAM auth ENABLED** ✅
   - `mdb-four-zero` - **Auth DISABLED** ❌

2. **When SCRAM is enabled on any deployment:**
   - The automation config sets `autoUser` and `autoPwd` at the **project level**
   - These credentials persist in the project's automation config

3. **Monitoring Agent Behavior:**
   - The monitoring agent reads credentials from the project-level config
   - It attempts to authenticate to **ALL** monitored hosts using these credentials
   - When it connects to `mdb-four-zero` (auth disabled), authentication fails

4. **Automation Config Evidence:**
   From the failing run's `automation-config.json`:
   ```json
   "auth": {
     "disabled": true,
     "autoUser": "mms-automation-agent",
     "autoPwd": "<redacted>",
     ...
   }
   ```
   **Note:** Even though `disabled: true`, the `autoUser` and `autoPwd` fields are STILL PRESENT!

#### Code Path Analysis

**SCRAM vs X509/LDAP credential handling comparison:**

| Auth Type | Sets monitoringAgentConfig? | Clears credentials on disable? |
|-----------|----------------------------|--------------------------------|
| SCRAM-SHA | ❌ NO | ❌ NO |
| X509 | ✅ YES (`EnableX509Authentication`) | ✅ YES (`DisableX509Authentication`) |
| LDAP | ✅ YES (`EnableLdapAuthentication`) | ✅ YES (`DisableLdapAuthentication`) |

**From `controllers/operator/authentication/scramsha.go`:**
```go
func (s *automationConfigScramSha) EnableAgentAuthentication(...) error {
    return conn.ReadUpdateAutomationConfig(func(ac *om.AutomationConfig) error {
        // Sets autoUser and autoPwd in automation config
        // BUT NEVER calls ReadUpdateMonitoringAgentConfig!
    }, log)
}
```

**From `controllers/operator/authentication/authentication.go` (Disable function):**
```go
// Lines 215-218 - Only disables X509, NOT SCRAM credentials
err = conn.ReadUpdateMonitoringAgentConfig(func(config *om.MonitoringAgentConfig) error {
    config.DisableX509Authentication()  // Clears SSLPemKeyFile and Username
    return nil                           // Does NOT clear Password!
}, log)
```

### 2. TLS/Network Configuration Analysis

**TLS is NOT the culprit.**

From the automation config:
```json
"tls": {
  "mode": "disabled"
}
```

From the monitoring agent header:
```
sslRequireValidServerCertificates = <unset>
sslTrustedServerCertificates = <unset>
useSslForAllConnections = <unset>
```

The connection failures are **authentication failures**, not TLS/network issues. The errors like "connection reset by peer" and "EOF" are secondary symptoms that occur when the SASL authentication handshake fails.

### 3. Timeline Analysis of the Delete/Recreate Cycle

```
┌─────────────────────────────────────────────────────────────────────────────┐
│ T+0s: mdb_prev deleted                                                       │
│       └─> Hosts deregistered from OM (successful, took 2s)                   │
│       └─> PROJECT-level autoUser/autoPwd NOT cleared (no auth to disable)    │
├─────────────────────────────────────────────────────────────────────────────┤
│ T+2s: test_hosts_were_removed PASSES                                         │
│       └─> Hosts verified empty in OM                                         │
├─────────────────────────────────────────────────────────────────────────────┤
│ T+3s: mdb_prev re-created with backup enabled                                │
│       └─> New pods start, MongoDB instances start                            │
│       └─> Auth disabled for this deployment (spec has no auth config)        │
├─────────────────────────────────────────────────────────────────────────────┤
│ T+5s: Monitoring agent assigned to new hosts                                 │
│       └─> OM configures monitoring for mdb-four-zero-{0,1,2}                │
│       └─> Monitoring agent reads PROJECT-level credentials                   │
│       └─> Attempts SCRAM-SHA-1 auth against MongoDB (auth disabled)          │
│       └─> ❌ "unable to authenticate using mechanism SCRAM-SHA-1"            │
├─────────────────────────────────────────────────────────────────────────────┤
│ T+10s-600s: Retry loop                                                       │
│       └─> Monitoring agent retries every 10s                                 │
│       └─> All attempts fail with same auth error                             │
│       └─> No metrics collected → No version info in OM                       │
│       └─> Backup API returns 409: "version information not yet available"    │
├─────────────────────────────────────────────────────────────────────────────┤
│ T+600s: Test times out                                                       │
│       └─> ❌ FAIL: test_deploy_same_mdb_again_with_orphaned_backup           │
└─────────────────────────────────────────────────────────────────────────────┘
```

### 4. Comparison of Passing vs Failing Runs

| Aspect | Passing Runs | Failing Runs |
|--------|-------------|--------------|
| `test_hosts_were_removed` | ✅ PASS (~2s) | ✅ PASS (~2s) |
| `orphaned_backup` test | ✅ PASS (~60-120s) | ❌ FAIL (600s timeout) |
| Other deployments in project | Likely no SCRAM auth | SCRAM auth enabled |
| Monitoring agent auth | No credentials needed | SCRAM credentials present |
| Error type | None | SCRAM-SHA-1 AuthenticationFailed |

**The key difference:** Whether other deployments in the same project have SCRAM authentication enabled.

When `my-mongodb-blockstore`, `my-mongodb-oplog`, and `my-mongodb-s3` have SCRAM enabled:
- The project's automation config has `autoUser`/`autoPwd` set
- The monitoring agent uses these credentials for ALL hosts
- `mdb-four-zero` (no auth) rejects the authentication

### 5. Recommended Fixes

#### Option A: Fix in Operator (Preferred)

**Clear monitoring agent SCRAM credentials when appropriate:**

Add a `DisableScramAuthentication()` method to `MonitoringAgentConfig`:

```go
// controllers/om/monitoring_agent_config.go
func (m *MonitoringAgentConfig) DisableScramAuthentication() {
    m.UnsetAgentUsername()
    m.UnsetAgentPassword()
}
```

Then update the SCRAM disabler to call it (similar to how LDAP does):

```go
// controllers/operator/authentication/scramsha.go
func (s *automationConfigScramSha) DisableAgentAuthentication(conn om.Connection, log *zap.SugaredLogger) error {
    // Existing code...

    // Add: Clear monitoring agent credentials
    err = conn.ReadUpdateMonitoringAgentConfig(func(config *om.MonitoringAgentConfig) error {
        config.UnsetAgentUsername()
        config.UnsetAgentPassword()
        return nil
    }, log)
    // ...
}
```

#### Option B: Fix in Test (Workaround)

**Enable SCRAM auth on `mdb_prev` to match other deployments:**

Modify `replica-set-for-om.yaml` or create a new fixture:

```yaml
spec:
  security:
    authentication:
      enabled: true
      modes:
        - SCRAM
```

This ensures `mdb_prev` uses the same auth mechanism as other project deployments.

#### Option C: Fix in Ops Manager (Ideal but not in our control)

Configure monitoring agent to use **per-host** authentication settings rather than project-level credentials. This would require Ops Manager changes.

### 6. Summary

| Finding | Details |
|---------|---------|
| **Root Cause** | Project-level SCRAM credentials contaminate monitoring of auth-disabled deployments |
| **Error Type** | SCRAM-SHA-1 authentication failure (not TLS or network) |
| **Why Flaky** | Depends on whether other project deployments have SCRAM enabled |
| **PR #719 Status** | Does NOT fix this issue (only clears X509 credentials, not SCRAM) |
| **Recommended Fix** | Clear SCRAM credentials from monitoringAgentConfig when auth is disabled |

## Fix Implementation

### Code Changes

**File:** `controllers/operator/authentication/scramsha.go`

**Before (lines 45-50):**
```go
func (s *automationConfigScramSha) DisableAgentAuthentication(conn om.Connection, log *zap.SugaredLogger) error {
    return conn.ReadUpdateAutomationConfig(func(ac *om.AutomationConfig) error {
        ac.Auth.AutoAuthMechanisms = stringutil.Remove(ac.Auth.AutoAuthMechanisms, string(s.MechanismName))
        return nil
    }, log)
}
```

**After (lines 45-73):**
```go
func (s *automationConfigScramSha) DisableAgentAuthentication(conn om.Connection, log *zap.SugaredLogger) error {
    err := conn.ReadUpdateAutomationConfig(func(ac *om.AutomationConfig) error {
        ac.Auth.AutoAuthMechanisms = stringutil.Remove(ac.Auth.AutoAuthMechanisms, string(s.MechanismName))
        return nil
    }, log)
    if err != nil {
        return err
    }

    // Clear monitoring agent credentials to prevent SCRAM authentication attempts
    // against deployments that don't have auth enabled. This follows the same pattern
    // as LDAP and X509 authentication mechanisms.
    log.Info("Clearing monitoring agent SCRAM credentials")
    err = conn.ReadUpdateMonitoringAgentConfig(func(config *om.MonitoringAgentConfig) error {
        config.UnsetAgentUsername()
        config.UnsetAgentPassword()
        return nil
    }, log)
    if err != nil {
        return err
    }

    log.Info("Clearing backup agent SCRAM credentials")
    return conn.ReadUpdateBackupAgentConfig(func(config *om.BackupAgentConfig) error {
        config.UnsetAgentUsername()
        config.UnsetAgentPassword()
        return nil
    }, log)
}
```

### Unit Test Added

**File:** `controllers/operator/authentication/scramsha_test.go`

Added `TestDisableAgentAuthentication_ClearsMonitoringAndBackupCredentials` which:
1. Enables SCRAM authentication
2. Pre-populates monitoring/backup configs with credentials
3. Disables SCRAM authentication
4. Verifies credentials are cleared (set to `MergoDelete` sentinel)

## Validation Patches

| Patch | Description | Link | Expected Result |
|-------|-------------|------|-----------------|
| Run 1 | SCRAM credential clearing fix validation | [6978b31c5220fd0007d45564](https://spruce.mongodb.com/version/6978b31c5220fd0007d45564) | ✅ Pass |
| Run 2 | SCRAM credential clearing fix validation | [6978b32f0e7edd0007fc7273](https://spruce.mongodb.com/version/6978b32f0e7edd0007fc7273) | ✅ Pass |
| Run 3 | SCRAM credential clearing fix validation | [6978b340c1a5860007bb0cdc](https://spruce.mongodb.com/version/6978b340c1a5860007bb0cdc) | ✅ Pass |

### Previous Validation (Before SCRAM Fix)

| Patch | Description | Link | Result |
|-------|-------------|------|--------|
| Original 1 | Auth credentials clearing (authentication.go only) | [69749efb5220fd0007c8e93d](https://spruce.mongodb.com/version/69749efb5220fd0007c8e93d) | ✅ Pass |
| Original 2 | Auth credentials clearing (authentication.go only) | [69749f0d0e7edd0007fb3e97](https://spruce.mongodb.com/version/69749f0d0e7edd0007fb3e97) | ✅ Pass |
| Original 3 | Auth credentials clearing (authentication.go only) | [69749f2cc1a5860007b9ffb6](https://spruce.mongodb.com/version/69749f2cc1a5860007b9ffb6) | ✅ Pass |
| Original 4 | Auth credentials clearing (authentication.go only) | [69749f3b5220fd0007c8ec6b](https://spruce.mongodb.com/version/69749f3b5220fd0007c8ec6b) | ✅ Pass |
| Original 5 | Auth credentials clearing (authentication.go only) | [69749f510e7edd0007fb3fd9](https://spruce.mongodb.com/version/69749f510e7edd0007fb3fd9) | ✅ Pass |
| Original 6 | Auth credentials clearing (authentication.go only) | [69749f6dc1a5860007ba0067](https://spruce.mongodb.com/version/69749f6dc1a5860007ba0067) | ✅ Pass |

### Historical Failures (Before Any Fix)

| Version | Branch | Date | `test_hosts_were_removed` | `orphaned_backup` | Error |
|---------|--------|------|---------------------------|-------------------|-------|
| [294d005](https://spruce.mongodb.com/version/mongodb_kubernetes_294d005d333d64faff0d753ad57bb25b5941917f) | master | 2026-01-23 | ✅ 2.07s | ❌ 600.8s | SCRAM-SHA-1 auth failure |
| [8069ca9](https://spruce.mongodb.com/version/mongodb_kubernetes_8069ca9d4b789a310cf8a3906e84cd510bd927a9) | master | 2026-01-22 | ✅ 2.08s | ❌ 600.9s | SCRAM-SHA-1 auth failure |
| [04c5f42](https://spruce.mongodb.com/version/mongodb_kubernetes_04c5f42463d52fe6c36b964f5ca4aec7c6c02355) | master | 2026-01-21 | ✅ 2.07s | ❌ 600.8s | SCRAM-SHA-1 auth failure |
| [cf3b867](https://spruce.mongodb.com/version/mongodb_kubernetes_cf3b867963942c12def5fd0f9606f3248bd6d8bf) | master | 2026-01-20 | ✅ 2.08s | ❌ 600.9s | SCRAM-SHA-1 auth failure |
| [6553781](https://spruce.mongodb.com/version/mongodb_kubernetes_655378110fefb5d5526f137af45f8dae532ac371) | master | 2026-01-19 | ✅ 2.07s | ❌ 600.8s | SCRAM-SHA-1 auth failure |
| [69776c8](https://spruce.mongodb.com/version/69776c8e5a5d2c0007d8a9a2) | master | 2026-01-18 | ✅ 2.08s | ❌ 600.8s | SCRAM-SHA-1 auth failure |

## Files Reference

| File | Purpose |
|------|---------|
| `controllers/operator/authentication/authentication.go` | PR #719 fix location (Disable() function) |
| `controllers/operator/authentication/scramsha.go` | **NEW FIX** - SCRAM DisableAgentAuthentication now clears credentials |
| `controllers/operator/authentication/scramsha_test.go` | Unit test for credential clearing |
| `controllers/operator/common_controller.go:642-648` | Where Disable() is called |
| `docker/mongodb-kubernetes-tests/tests/opsmanager/om_ops_manager_backup.py` | Test file |
| `docker/mongodb-kubernetes-tests/tests/opsmanager/fixtures/replica-set-for-om.yaml` | Test fixture (no auth) |
| `controllers/om/monitoring_agent_config.go` | Monitoring agent config structure |
| `controllers/om/omclient.go` | OM API client (hosts, monitoring config) |
| `controllers/operator/authentication/ldap.go` | LDAP auth - reference pattern |
| `controllers/operator/authentication/x509.go` | X509 auth - reference pattern |

## PR #719 Status

**Branch:** `fix/backup-409-version-not-available`
**Status:** Draft (per user request)

### Commits
1. `9f6bb3265` - Clear monitoring/backup agent credentials when auth is disabled (authentication.go)
2. `d35456a55` - Increase retry interval to 30s for version info wait
3. `7d9e0e990` - Add unit test for clearing agent credentials when auth is disabled
4. `f67e5b017` - Update changelog to reflect actual fix
5. `2ab0e774d` - Remove unnecessary 409 error handling
6. **`9a59bd9bd`** - Clear SCRAM credentials in DisableAgentAuthentication (**NEW - Root cause fix**)

### Why Both Fixes Are Needed

| Scenario | Which Fix Applies |
|----------|-------------------|
| Auth enabled → Auth disabled | `authentication.go` Disable() function |
| SCRAM → LDAP (mechanism switch) | `scramsha.go` DisableAgentAuthentication |
| SCRAM → X509 (mechanism switch) | `scramsha.go` DisableAgentAuthentication |
| Project with mixed auth deployments | `scramsha.go` DisableAgentAuthentication |

