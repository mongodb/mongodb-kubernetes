package automationconfig

import (
	"context"
	"reflect"
	"testing"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/mock"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/kube"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/secret"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestComputeSecret_CreateNew checks the "create" features of 'ensureAutomationConfigSecret' function when the secret is created
// if it doesn't exist (or the creation is skipped totally)
func TestEnsureAutomationConfigSecret_CreateNew(t *testing.T) {
	client := mock.NewClient()
	owner := mdbv1.MongoDB{ObjectMeta: metav1.ObjectMeta{Name: "test"}}
	key := kube.ObjectKey("ns", "cfm")
	testData := map[string][]byte{"foo": []byte("bar")}

	// Successful creation
	createdSecret, err := EnsureSecret(client, key, func(secret *corev1.Secret) bool {
		secret.Data = testData
		return true
	}, &owner)

	assert.NoError(t, err)

	s := &corev1.Secret{}
	err = client.Get(context.TODO(), key, s)
	assert.NoError(t, err)
	assert.Equal(t, createdSecret, *s)
	assert.Equal(t, key.Name, s.Name)
	assert.Equal(t, key.Namespace, s.Namespace)
	assert.Equal(t, "test", s.OwnerReferences[0].Name)
	assert.Equal(t, testData, s.Data)

	// Creation skipped
	key2 := kube.ObjectKey("ns", "cfm2")
	_, err = EnsureSecret(client, key2, func(s *corev1.Secret) bool {
		return false
	}, &owner)

	assert.NoError(t, err)
	err = client.Get(context.TODO(), key2, s)
	assert.True(t, apiErrors.IsNotFound(err))
}

func TestEnsureAutomationConfig_UpdateExisting(t *testing.T) {
	client := mock.NewClient()
	err := client.CreateSecret(secret.Builder().
		SetNamespace(mock.TestNamespace).
		SetName("secret-name").
		SetField(util.OmBaseUrl, "http://mycompany.com:8080").
		SetField(util.OmProjectName, "project-name").
		SetField(util.OmOrgId, "org-id").
		Build(),
	)
	assert.NoError(t, err)

	owner := mdbv1.MongoDB{ObjectMeta: metav1.ObjectMeta{Name: "test"}}

	key := kube.ObjectKey(mock.TestNamespace, "secret-name")

	// Successful update (data is appended)
	_, err = EnsureSecret(client, key, func(s *corev1.Secret) bool {
		s.Data["foo"] = []byte("bla")
		return true
	}, &owner)

	assert.NoError(t, err)

	s := &corev1.Secret{}
	err = client.Get(context.TODO(), key, s)
	assert.NoError(t, err)
	// We don't change the owner in case of update
	assert.Empty(t, s.OwnerReferences)
	// We added one key-value but the other must stay in the config map
	assert.True(t, len(s.Data) > 1)

	currentSize := len(s.Data)

	// Update skipped
	_, err = EnsureSecret(client, key, func(s *corev1.Secret) bool {
		return false
	}, &owner)

	assert.NoError(t, err)

	s = &corev1.Secret{}
	err = client.Get(context.TODO(), key, s)
	assert.NoError(t, err)
	// The size of data must not change as there was no update
	assert.Len(t, s.Data, currentSize)

	// The only operation in history is the first update
	client.CheckNumberOfOperations(t, mock.HItem(reflect.ValueOf(client.Update), s), 1)
}
