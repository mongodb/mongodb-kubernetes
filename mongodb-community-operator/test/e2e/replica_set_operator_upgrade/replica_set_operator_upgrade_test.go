package replica_set_operator_upgrade

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1"
	e2eutil "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/test/e2e"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/test/e2e/mongodbtests"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/test/e2e/setup"
	. "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/test/e2e/util/mongotester"
)

func TestMain(m *testing.M) {
	code, err := e2eutil.RunTest(m)
	if err != nil {
		fmt.Println(err)
	}
	os.Exit(code)
}

func TestReplicaSetOperatorUpgradeMCOToMCK(t *testing.T) {
	ctx := context.Background()
	resourceName := "mdb0"
	testConfig := setup.LoadTestConfigFromEnv()
	testCtx := setup.SetupWithTestConfigNoOperator(ctx, t, testConfig, false)
	defer testCtx.Teardown()

	// Step 1: Install the latest community operator using public MongoDB Helm chart

	err := setup.InstallCommunityOperatorViaHelm(ctx, t, testConfig, testConfig.Namespace)
	require.NoError(t, err)

	mdb, user := e2eutil.NewTestMongoDB(testCtx, resourceName, testConfig.Namespace)
	mdb.Spec.Version = "6.0.5"
	mdb.Spec.Arbiters = 1
	mdb.Spec.Members = 2

	_, err = setup.GeneratePasswordForUser(testCtx, user, testConfig.Namespace)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("Create MongoDB Resource", mongodbtests.CreateMongoDBResource(&mdb, testCtx))
	t.Run("Basic tests with community operator", mongodbtests.BasicFunctionality(ctx, &mdb, true))
	t.Run("AutomationConfig has the correct version", mongodbtests.AutomationConfigVersionHasTheExpectedVersion(ctx, &mdb, 1))

	tester, err := FromResource(ctx, t, mdb)
	if err != nil {
		t.Fatal(err)
	}

	// Step 2: Scale down the MCO operator deployment to prevent it from running its reconciler
	t.Log("Step 1: Scaling down MCO operator deployment")
	err = setup.ScaleOperatorDeployment(ctx, t, testConfig.Namespace, setup.CommunityHelmChartAndDeploymentName, 0)
	assert.NoError(t, err)

	// Step 3: Install the new MCK chart
	t.Log("Step 2: Installing MCK operator")
	err = setup.DeployMCKOperator(ctx, t, testConfig, resourceName, false, false, setup.HelmArg{
		Name:  "operator.name",
		Value: setup.MCKHelmChartAndDeploymentName,
	})
	assert.NoError(t, err)

	// let's check whether all is healthy and fine
	t.Run("Basic tests with community operator", mongodbtests.BasicFunctionality(ctx, &mdb, true))

	t.Log("Successfully migrated from MCO to MCK")

	// Step 4: Remove the MCO chart now that migration is complete
	t.Log("Step 8: Uninstalling MCO chart (CRDs will remain)")
	err = setup.UninstallCommunityOperatorViaHelm(ctx, t, testConfig.Namespace)
	assert.NoError(t, err, "Failed to uninstall MCO chart")

	// Verify functionality after migration to MCK
	t.Run("Basic tests after migration to MCK", mongodbtests.BasicFunctionality(ctx, &mdb, true))
	t.Run("MongoDB is reachable after migration", func(t *testing.T) {
		defer tester.StartBackgroundConnectivityTest(t, time.Second*10)()
		t.Run("Scale MongoDB Resource Up", mongodbtests.Scale(ctx, &mdb, 5))
		t.Run("Stateful Set Scaled Up Correctly", mongodbtests.StatefulSetBecomesReady(ctx, &mdb))
		t.Run("MongoDB Reaches Running Phase", mongodbtests.MongoDBReachesRunningPhase(ctx, &mdb))
		t.Run("AutomationConfig's version has been increased", mongodbtests.AutomationConfigVersionHasTheExpectedVersion(ctx, &mdb, 4)) // 4, because the mck upgrade already forces one version bump
		t.Run("Test Status Was Updated", mongodbtests.Status(ctx, &mdb, mdbv1.MongoDBCommunityStatus{
			MongoURI:                           mdb.MongoURI(),
			Phase:                              mdbv1.Running,
			Version:                            mdb.GetMongoDBVersion(),
			CurrentMongoDBMembers:              5,
			CurrentStatefulSetReplicas:         5,
			CurrentStatefulSetArbitersReplicas: 1,
			CurrentMongoDBArbiters:             1,
		}))
	})
}

func TestReplicaSetOperatorUpgrade(t *testing.T) {
	t.Skipf("Skipping test until CLOUDP-308559 has been fixed, we should also update this test to update from MCO to MCK")
	ctx := context.Background()
	resourceName := "mdb0"
	testConfig := setup.LoadTestConfigFromEnv()
	testCtx := setup.SetupWithTestConfig(ctx, t, testConfig, true, true, resourceName)
	defer testCtx.Teardown()

	mdb, user := e2eutil.NewTestMongoDB(testCtx, resourceName, testConfig.Namespace)
	// Prior operator versions did not support MDB7
	mdb.Spec.Version = "6.0.5"
	scramUser := mdb.GetAuthUsers()[0]
	mdb.Spec.Security.TLS = e2eutil.NewTestTLSConfig(false)
	mdb.Spec.Arbiters = 1
	mdb.Spec.Members = 2

	_, err := setup.GeneratePasswordForUser(testCtx, user, testConfig.Namespace)
	if err != nil {
		t.Fatal(err)
	}

	tester, err := FromResource(ctx, t, mdb)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("Create MongoDB Resource", mongodbtests.CreateMongoDBResource(&mdb, testCtx))
	t.Run("Basic tests", mongodbtests.BasicFunctionality(ctx, &mdb, true))
	t.Run("AutomationConfig has the correct version", mongodbtests.AutomationConfigVersionHasTheExpectedVersion(ctx, &mdb, 1))
	mongodbtests.SkipTestIfLocal(t, "Ensure MongoDB TLS Configuration", func(t *testing.T) {
		t.Run("Has TLS Mode", tester.HasTlsMode("requireSSL", 60, WithTls(ctx, mdb)))
		t.Run("Basic Connectivity Succeeds", tester.ConnectivitySucceeds(WithTls(ctx, mdb)))
		t.Run("SRV Connectivity Succeeds", tester.ConnectivitySucceeds(WithURI(mdb.MongoSRVURI("")), WithTls(ctx, mdb)))
		t.Run("Basic Connectivity With Generated Connection String Secret Succeeds",
			tester.ConnectivitySucceeds(WithURI(mongodbtests.GetConnectionStringForUser(ctx, mdb, scramUser)), WithTls(ctx, mdb)))
		t.Run("SRV Connectivity With Generated Connection String Secret Succeeds",
			tester.ConnectivitySucceeds(WithURI(mongodbtests.GetSrvConnectionStringForUser(ctx, mdb, scramUser)), WithTls(ctx, mdb)))
		t.Run("Connectivity Fails", tester.ConnectivityFails(WithoutTls()))
		t.Run("Ensure authentication is configured", tester.EnsureAuthenticationIsConfigured(3, WithTls(ctx, mdb)))
	})

	// upgrade the operator to master
	config := setup.LoadTestConfigFromEnv()
	err = setup.DeployMCKOperator(ctx, t, config, resourceName, true, false)
	assert.NoError(t, err)

	// Perform the basic tests
	t.Run("Basic tests", mongodbtests.BasicFunctionality(ctx, &mdb, true))
}

// TestReplicaSetOperatorUpgradeFrom0_7_2 is intended to be run locally not in CI.
// It simulates deploying cluster using community operator 0.7.2 and then upgrading it using newer version.
func TestReplicaSetOperatorUpgradeFrom0_7_2(t *testing.T) {
	ctx := context.Background() //nolint
	t.Skip("Supporting this test in CI requires installing also CRDs from release v0.7.2")
	resourceName := "mdb-upg"
	testConfig := setup.LoadTestConfigFromEnv()

	// deploy operator and other components as it was at version 0.7.2
	testConfig.OperatorImageRepoUrl = "quay.io/mongodb"
	testConfig.OperatorImage = "mongodb-kubernetes-operator"
	testConfig.OperatorVersion = "0.7.2"
	testConfig.VersionUpgradeHookImage = "quay.io/mongodb/mongodb-kubernetes-operator-version-upgrade-post-start-hook:1.0.3"
	testConfig.ReadinessProbeImage = "quay.io/mongodb/mongodb-kubernetes-readinessprobe:1.0.6"
	testConfig.AgentImage = "quay.io/mongodb/mongodb-agent:11.0.5.6963-1"

	testCtx := setup.SetupWithTestConfig(ctx, t, testConfig, true, false, resourceName)
	defer testCtx.Teardown()

	mdb, user := e2eutil.NewTestMongoDB(testCtx, resourceName, "")
	scramUser := mdb.GetAuthUsers()[0]
	mdb.Spec.Security.TLS = e2eutil.NewTestTLSConfig(false)

	_, err := setup.GeneratePasswordForUser(testCtx, user, "")
	if err != nil {
		t.Fatal(err)
	}

	tester, err := FromResource(ctx, t, mdb)
	if err != nil {
		t.Fatal(err)
	}

	runTests := func(t *testing.T) {
		t.Run("Create MongoDB Resource", mongodbtests.CreateMongoDBResource(&mdb, testCtx))
		t.Run("Basic tests", mongodbtests.BasicFunctionality(ctx, &mdb, true))
		t.Run("AutomationConfig has the correct version", mongodbtests.AutomationConfigVersionHasTheExpectedVersion(ctx, &mdb, 1))
		t.Run("Keyfile authentication is configured", tester.HasKeyfileAuth(3))
		t.Run("Has TLS Mode", tester.HasTlsMode("requireSSL", 60, WithTls(ctx, mdb)))
		t.Run("Test Basic Connectivity", tester.ConnectivitySucceeds())
		t.Run("Test SRV Connectivity", tester.ConnectivitySucceeds(WithURI(mdb.MongoSRVURI("")), WithoutTls(), WithReplicaSet(mdb.Name)))
		t.Run("Test Basic Connectivity with generated connection string secret",
			tester.ConnectivitySucceeds(WithURI(mongodbtests.GetConnectionStringForUser(ctx, mdb, scramUser))))
		t.Run("Test SRV Connectivity with generated connection string secret",
			tester.ConnectivitySucceeds(WithURI(mongodbtests.GetSrvConnectionStringForUser(ctx, mdb, scramUser))))
		t.Run("Ensure Authentication", tester.EnsureAuthenticationIsConfigured(3))
	}

	runTests(t)

	// When running against local operator we could stop here,
	// rescale helm operator deployment to zero and run local operator then.

	testConfig = setup.LoadTestConfigFromEnv()
	err = setup.DeployMCKOperator(ctx, t, testConfig, resourceName, true, false)
	assert.NoError(t, err)

	runTests(t)
}
