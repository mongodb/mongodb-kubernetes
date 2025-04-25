package replica_set_cross_namespace_deploy

import (
	"context"
	"fmt"
	"os"
	"testing"

	"k8s.io/apimachinery/pkg/types"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"

	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/util/generate"
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

func TestCrossNamespaceDeploy(t *testing.T) {
	ctx := context.Background()
	testCtx := setup.Setup(ctx, t)
	defer testCtx.Teardown()

	postfix, err := generate.RandomValidDNS1123Label(5)
	if err != nil {
		t.Fatal(err)
	}
	namespace := fmt.Sprintf("clusterwide-test-%s", postfix)

	err = e2eutil.EnsureNamespace(testCtx, namespace)
	if err != nil {
		t.Fatal(err)
	}

	if err := createDatabaseServiceAccountRoleAndRoleBinding(ctx, t, namespace); err != nil {
		t.Fatal(err)
	}

	mdb, user := e2eutil.NewTestMongoDB(testCtx, "mdb0", namespace)

	_, err = setup.GeneratePasswordForUser(testCtx, user, namespace)
	if err != nil {
		t.Fatal(err)
	}

	tester, err := FromResource(ctx, t, mdb)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("Create MongoDB Resource", mongodbtests.CreateMongoDBResource(&mdb, testCtx))
	t.Run("Basic tests", mongodbtests.BasicFunctionality(ctx, &mdb))
	t.Run("Keyfile authentication is configured", tester.HasKeyfileAuth(3))
	t.Run("Test Basic Connectivity", tester.ConnectivitySucceeds())
	t.Run("Ensure Authentication", tester.EnsureAuthenticationIsConfigured(3))
	t.Run("AutomationConfig has the correct version", mongodbtests.AutomationConfigVersionHasTheExpectedVersion(ctx, &mdb, 1))
}

// createDatabaseServiceAccountRoleAndRoleBinding creates the ServiceAccount, Role and RoleBinding required
// for the database StatefulSet in the other namespace.
func createDatabaseServiceAccountRoleAndRoleBinding(ctx context.Context, t *testing.T, namespace string) error {
	sa := corev1.ServiceAccount{}
	// TODO: MCK choose a correct SA name
	mckServiceAccountName := "mongodb-enterprise-appdb"
	err := e2eutil.TestClient.Get(ctx, types.NamespacedName{Name: mckServiceAccountName, Namespace: e2eutil.OperatorNamespace}, &sa)
	if err != nil {
		t.Fatal(err)
	}

	sa.Namespace = namespace
	sa.ResourceVersion = ""

	err = e2eutil.TestClient.Create(ctx, &sa, &e2eutil.CleanupOptions{})
	if err != nil {
		t.Fatal(err)
	}

	role := rbacv1.Role{}
	err = e2eutil.TestClient.Get(ctx, types.NamespacedName{Name: mckServiceAccountName, Namespace: e2eutil.OperatorNamespace}, &role)
	if err != nil {
		t.Fatal(err)
	}

	role.Namespace = namespace
	role.ResourceVersion = ""

	err = e2eutil.TestClient.Create(ctx, &role, &e2eutil.CleanupOptions{})
	if err != nil {
		t.Fatal(err)
	}

	rolebinding := rbacv1.RoleBinding{}
	err = e2eutil.TestClient.Get(ctx, types.NamespacedName{Name: mckServiceAccountName, Namespace: e2eutil.OperatorNamespace}, &rolebinding)
	if err != nil {
		t.Fatal(err)
	}

	rolebinding.Namespace = namespace
	rolebinding.ResourceVersion = ""

	// Update namespace in subjects field
	for i := range rolebinding.Subjects {
		if rolebinding.Subjects[i].Kind == "ServiceAccount" &&
			rolebinding.Subjects[i].Name == mckServiceAccountName {
			rolebinding.Subjects[i].Namespace = namespace
		}
	}
	err = e2eutil.TestClient.Create(ctx, &rolebinding, &e2eutil.CleanupOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return nil
}
