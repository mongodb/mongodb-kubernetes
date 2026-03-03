//go:build e2e

package opsmanager_e2e

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}

// TestAppDBMetaOMModeSwitchE2E validates the full headless → online agent transition.
//
// Scenario:
//  1. Deploy Primary OM + AppDB (headless mode) + sample MongoDB deployment
//  2. Deploy Meta OM + its own AppDB
//  3. Patch Primary OM with spec.applicationDatabase.managedByMetaOM
//  4. Assert AppDB pods restart and come back Ready
//  5. Assert env vars on AppDB pods reflect online mode
//  6. Assert sample MongoDB deployment is still healthy (no data loss / no connectivity disruption)
func TestAppDBMetaOMModeSwitchE2E(t *testing.T) {
	t.Skip("e2e: requires live cluster with two Ops Manager instances")

	t.Log("Step 1: Deploy Primary OM with headless AppDB")
	// TODO: create MongoDBOpsManager CR (Primary OM) with default headless AppDB config, wait for Running phase
	// TODO: insert a test document into AppDB to verify no data loss after the mode switch

	t.Log("Step 2: Deploy a sample MongoDB managed by Primary OM")
	// TODO: create MongoDB CR pointing to Primary OM's connection config map + credentials secret
	// TODO: wait for MongoDB CR to reach Running phase

	t.Log("Step 3: Deploy Meta OM")
	// TODO: create a second MongoDBOpsManager CR (Meta OM) with its own AppDB in a separate namespace (or same)
	// TODO: wait for Meta OM CR to reach Running phase

	t.Log("Step 4: Create Meta OM credentials Secret")
	// TODO: create a corev1.Secret with keys publicKey and privateKey populated from Meta OM admin user
	// TODO: the secret name must match credentialsSecretRef.name used in Step 5 patch

	t.Log("Step 5: Patch Primary OM to enable managedByMetaOM")
	// TODO: patch Primary OM CR by adding to spec.applicationDatabase:
	//   managedByMetaOM:
	//     name: <meta-om-cr-name>
	//     projectName: primary-appdb
	//     credentialsSecretRef:
	//       name: meta-om-creds
	// TODO: confirm the patch was accepted (no validation errors)

	t.Log("Step 6: Wait for AppDB pods to restart and become Ready")
	// TODO: watch the AppDB StatefulSet; wait for observed generation to bump (controller reacted to patch)
	// TODO: wait until all AppDB pods pass the Ready condition (readinessProbe succeeds)

	t.Log("Step 7: Assert env vars on AppDB pods")
	// TODO: read AppDB StatefulSet .spec.template.spec.containers[agent].env, assert:
	//   - MMS_SERVER is present and points to the Meta OM base URL
	//   - MMS_GROUP_ID is present (non-empty project ID allocated in Meta OM)
	//   - MMS_API_KEY is present (agent API key from Meta OM)
	//   - HEADLESS_AGENT env var is absent (no longer headless mode)
	//   - AUTOMATION_CONFIG_MAP env var is absent (config map handoff no longer used)

	t.Log("Step 8: Assert no data loss")
	// TODO: connect to AppDB using the same connection string used in Step 1
	// TODO: query the test document inserted in Step 1 and verify it is still present

	t.Log("Step 9: Assert sample MongoDB deployment still healthy")
	// TODO: re-fetch the MongoDB CR created in Step 2
	// TODO: assert its status.phase == Running (Primary OM is still managing it via its own agent)
}
