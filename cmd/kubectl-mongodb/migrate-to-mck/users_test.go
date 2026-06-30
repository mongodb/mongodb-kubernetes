package migratetomck

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/authentication"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
)

func newFakeSecret(name, password string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Data:       map[string][]byte{passwordSecretDataKey: []byte(password)},
	}
}

func newFakeKubeClient(objects ...runtime.Object) kubernetesClient.Client {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	clientObjs := make([]runtime.Object, len(objects))
	copy(clientObjs, objects)
	return kubernetesClient.NewClient(fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(clientObjs...).Build())
}

func writeTempFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "secrets.csv")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

func TestParseUsersSecretsFile_EmptyPath(t *testing.T) {
	m, err := parseUsersSecretsFile("")
	require.NoError(t, err)
	assert.Nil(t, m)
}

func TestParseUsersSecretsFile_Valid(t *testing.T) {
	content := `# comment
alice:admin,alice-secret
bob:mydb,bob-secret
`
	path := writeTempFile(t, content)
	m, err := parseUsersSecretsFile(path)
	require.NoError(t, err)
	require.Equal(t, "alice-secret", m["alice:admin"])
	require.Equal(t, "bob-secret", m["bob:mydb"])
}

func TestParseUsersSecretsFile_InvalidFormat(t *testing.T) {
	path := writeTempFile(t, "no-comma-here\n")
	_, err := parseUsersSecretsFile(path)
	assert.ErrorContains(t, err, "line 1")
}

func TestParseUsersSecretsFile_MissingDatabase(t *testing.T) {
	path := writeTempFile(t, "username-without-db,some-secret\n")
	_, err := parseUsersSecretsFile(path)
	assert.ErrorContains(t, err, "missing the database part")
}

func TestParseUsersSecretsFile_InvalidSecretName(t *testing.T) {
	path := writeTempFile(t, "alice:admin,Invalid_Name\n")
	_, err := parseUsersSecretsFile(path)
	assert.ErrorContains(t, err, "not a valid Kubernetes name")
}

func TestParseUsersSecretsFile_EmptyFields(t *testing.T) {
	path := writeTempFile(t, ",\n")
	_, err := parseUsersSecretsFile(path)
	assert.ErrorContains(t, err, "expected \"username:database,secret-name\"")
}

func TestGenerateUserCRs_ExistingSecrets(t *testing.T) {
	ac := om.NewAutomationConfig(om.Deployment{
		"processes":   []any{},
		"replicaSets": []any{},
	})
	ac.Auth.AutoUser = "mms-automation"
	ac.Auth.Users = []*om.MongoDBUser{
		{Username: "alice", Database: "admin", Roles: []*om.Role{{Role: "readWrite", Database: "myapp"}}},
	}

	opts := GenerateOptions{
		ExistingUserSecrets: map[string]string{
			"alice:admin": "alice-secret",
		},
	}
	users, err := GenerateUserCRs(ac, "my-rs", "default", opts)
	require.NoError(t, err)
	require.Len(t, users, 1, "Option 2 must not generate a Secret object")
	y, err := marshalCRToYAML(users[0])
	require.NoError(t, err)
	assert.Contains(t, y, "alice-secret")
}

func TestGenerateUserCRs_ExistingSecrets_SkipsUnmappedUsers(t *testing.T) {
	ac := om.NewAutomationConfig(om.Deployment{
		"processes":   []any{},
		"replicaSets": []any{},
	})
	ac.Auth.AutoUser = "mms-automation"
	ac.Auth.Users = []*om.MongoDBUser{
		{Username: "alice", Database: "admin", Roles: []*om.Role{{Role: "read", Database: "test"}}},
		{Username: "bob", Database: "admin", Roles: []*om.Role{{Role: "read", Database: "test"}}},
	}

	opts := GenerateOptions{
		ExistingUserSecrets: map[string]string{
			"alice:admin": "alice-secret",
		},
	}
	users, err := GenerateUserCRs(ac, "my-rs", "default", opts)
	require.NoError(t, err)
	require.Len(t, users, 1)
	y, err := marshalCRToYAML(users[0])
	require.NoError(t, err)
	assert.Contains(t, y, "alice")
}

func TestCollectUserSecretNamesInteractively_ExplicitName(t *testing.T) {
	ac := om.NewAutomationConfig(om.Deployment{
		"processes":   []any{},
		"replicaSets": []any{},
	})
	ac.Auth.AutoUser = "mms-automation"
	alice := &om.MongoDBUser{Username: "alice", Database: "admin", Roles: []*om.Role{{Role: "read", Database: "test"}}}
	ac.Auth.Users = []*om.MongoDBUser{alice}
	_, err := authentication.ConfigureScramCredentials(alice, "correct-password", ac)
	require.NoError(t, err)

	secret := newFakeSecret("my-alice-secret", "correct-password")
	kubeClient := newFakeKubeClient(secret)
	opts := &GenerateOptions{Namespace: "default"}
	scanner := bufio.NewScanner(strings.NewReader("my-alice-secret\n"))
	err = collectUserSecretNamesInteractively(t.Context(), kubeClient, ac, opts, scanner)
	require.NoError(t, err)
	assert.Equal(t, "my-alice-secret", opts.ExistingUserSecrets["alice:admin"])
}

func TestCollectUserSecretNamesInteractively_DefaultName(t *testing.T) {
	ac := om.NewAutomationConfig(om.Deployment{
		"processes":   []any{},
		"replicaSets": []any{},
	})
	ac.Auth.AutoUser = "mms-automation"
	alice := &om.MongoDBUser{Username: "alice", Database: "admin", Roles: []*om.Role{{Role: "read", Database: "test"}}}
	ac.Auth.Users = []*om.MongoDBUser{alice}
	_, err := authentication.ConfigureScramCredentials(alice, "correct-password", ac)
	require.NoError(t, err)

	// suggested name is "alice-password"
	secret := newFakeSecret("alice-password", "correct-password")
	kubeClient := newFakeKubeClient(secret)
	opts := &GenerateOptions{Namespace: "default"}
	scanner := bufio.NewScanner(strings.NewReader("\n")) // press Enter to accept suggested name
	err = collectUserSecretNamesInteractively(t.Context(), kubeClient, ac, opts, scanner)
	require.NoError(t, err)
	assert.Equal(t, "alice-password", opts.ExistingUserSecrets["alice:admin"])
}

func TestCollectUserSecretNamesInteractively_NoUsers(t *testing.T) {
	ac := om.NewAutomationConfig(om.Deployment{
		"processes":   []any{},
		"replicaSets": []any{},
	})
	ac.Auth.AutoUser = "mms-automation"
	opts := &GenerateOptions{Namespace: "default"}
	scanner := bufio.NewScanner(strings.NewReader(""))
	err := collectUserSecretNamesInteractively(t.Context(), nil, ac, opts, scanner)
	require.NoError(t, err)
	assert.Nil(t, opts.ExistingUserSecrets)
}
