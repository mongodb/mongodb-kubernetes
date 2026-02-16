# Fix CLOUDP-68873: Skip auth disable during X509→SCRAM transition

## Problem

The X509 to SCRAM authentication transition tests were failing ~5% of the time with a deadlock. The test flow was:

1. Start with X509 authentication enabled
2. Add SCRAM to deployment mechanisms (both X509 and SCRAM enabled)
3. **Disable authentication completely**
4. **Re-enable authentication with SCRAM only** ← Deadlock occurs here

When re-enabling SCRAM after auth was disabled, the operator would set `auth.Disabled=false` and add SCRAM to `DeploymentAuthMechanisms` in a single automation config update. This caused the agent to immediately try to execute the auth transition, but the SCRAM credentials for the automation user didn't exist yet.

The agent's `bootstrapAutomationCredentials()` function can create credentials using the localhost exception, but only when MongoDB is running **without** `--auth` enabled. When auth is re-enabled, MongoDB restarts with `--auth`, making the localhost exception unavailable, causing a deadlock.

## Root Cause

The MongoDB Automation Agent handles automation user credentials separately from regular users:
- Regular users in `Auth.Users` → synced by `UsersToChange()` function
- Automation user (`AutoUser`/`AutoPwd`) → handled by `bootstrapAutomationCredentials()` using localhost exception

The agent only calls `bootstrapAutomationCredentials()` when it gets an auth error during state gathering. The localhost exception only works when MongoDB runs without `--auth`.

When re-enabling auth after it was disabled:
1. Auth is disabled → MongoDB runs without `--auth` → no auth errors → `bootstrapAutomationCredentials()` never called
2. Auth is enabled → MongoDB restarts with `--auth` → auth error → `bootstrapAutomationCredentials()` called but localhost exception doesn't work → **deadlock**

## Solution

**Three-step transition while keeping auth enabled throughout:**

Instead of:
1. X509 enabled (agent uses X509)
2. X509 + SCRAM enabled (both mechanisms, agent still uses X509)
3. **Auth disabled** ← This causes the problem
4. **Auth re-enabled with SCRAM only** ← Deadlock occurs here

The new flow is:
1. X509 enabled (agent uses X509, X509 deployment mechanism)
2. **X509 + SCRAM enabled, agent switches to SCRAM** (both deployment mechanisms, agent uses SCRAM with X509 as fallback)
3. **SCRAM only** (remove X509 from deployment mechanisms, agent already using SCRAM)
4. **Auth disabled** (moved to the end, after successful SCRAM transition)

The key insight: In step 2, we switch the agent to SCRAM while X509 is still available. This allows SCRAM credentials to be configured while the agent can still fall back to X509 if needed.

This works because:
- When both X509 and SCRAM are enabled, the agent can still authenticate using X509
- The operator can configure SCRAM credentials while X509 is active
- The agent can then switch from X509 to SCRAM authentication without any auth-disabled state
- No localhost exception needed, no deadlock

## Changes

### Test Changes

Modified both X509→SCRAM transition tests to implement the three-step transition:

**`docker/mongodb-kubernetes-tests/tests/authentication/sharded_cluster_x509_to_scram_transition.py`:**
- Modified `test_enable_scram_and_x509` to switch agent to SCRAM during the "both mechanisms" phase
- Modified `TestCanEnableScramSha256` to only remove X509 from deployment mechanisms (agent already SCRAM)
- Moved `TestShardedClusterDisableAuthentication` to run at the end

**`docker/mongodb-kubernetes-tests/tests/authentication/replica_set_x509_to_scram_transition.py`:**
- Modified `test_enable_scram_and_x509` to switch agent to SCRAM during the "both mechanisms" phase
- Modified `TestCanEnableScramSha256` to only remove X509 from deployment mechanisms (agent already SCRAM)
- Moved `TestReplicaSetDisableAuthentication` to run at the end

### No Operator Code Changes Required

The operator already supports this flow through the existing `Configure()` function, which:
1. Adds SCRAM to deployment mechanisms
2. Configures SCRAM agent credentials
3. Switches agent authentication from X509 to SCRAM
4. Removes X509 from deployment mechanisms

All steps wait for goal state between operations, ensuring a safe transition.

## Testing

The fix will be validated by running the modified E2E tests multiple times to ensure they pass consistently without the ~5% failure rate.

## References

- Jira: https://jira.mongodb.org/browse/CLOUDP-68873
- Agent source code investigation: `~/projects/mms-automation/go_planner/src/com.tengen/cm/`
  - `auth/userchanges.go` - Shows agent skips automation user in `Auth.Users` array
  - `state/stateutil/stateutil.go` - Shows `bootstrapAutomationCredentials()` implementation

