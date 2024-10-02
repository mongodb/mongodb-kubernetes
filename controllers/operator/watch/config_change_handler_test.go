package watch

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"sigs.k8s.io/controller-runtime/pkg/event"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestShouldHandleUpdate(t *testing.T) {
	t.Run("Update shouldn't happen if ConfigMaps data hasn't changed", func(t *testing.T) {
		oldObj := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "name",
				Namespace: "ns",
			},
			Data: map[string]string{"testKey": "testValue"},
		}
		newObj := oldObj.DeepCopy()
		newObj.ObjectMeta.ResourceVersion = "4243"

		assert.False(t, shouldHandleUpdate(event.UpdateEvent{ObjectOld: oldObj, ObjectNew: newObj}))
	})
	t.Run("Update should happen if the data has changed for ConfigMap", func(t *testing.T) {
		oldObj := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "name",
				Namespace: "ns",
			},
			Data: map[string]string{"testKey": "testValue"},
		}
		newObj := oldObj.DeepCopy()
		newObj.ObjectMeta.ResourceVersion = "4243"
		newObj.Data["secondKey"] = "secondValue"

		assert.True(t, shouldHandleUpdate(event.UpdateEvent{ObjectOld: oldObj, ObjectNew: newObj}))
	})
	t.Run("Update shouldn't happen if Secrets data hasn't changed", func(t *testing.T) {
		oldObj := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "name",
				Namespace: "ns",
			},
			Data: map[string][]byte{"testKey": []byte("testValue")},
		}
		newObj := oldObj.DeepCopy()
		newObj.ObjectMeta.ResourceVersion = "4243"

		assert.False(t, shouldHandleUpdate(event.UpdateEvent{ObjectOld: oldObj, ObjectNew: newObj}))
	})
	t.Run("Update should happen if the data has changed for Secret", func(t *testing.T) {
		oldObj := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "name",
				Namespace: "ns",
			},
			Data: map[string][]byte{"testKey": []byte("testValue")},
		}
		newObj := oldObj.DeepCopy()
		newObj.ObjectMeta.ResourceVersion = "4243"
		newObj.Data["secondKey"] = []byte("secondValue")

		assert.True(t, shouldHandleUpdate(event.UpdateEvent{ObjectOld: oldObj, ObjectNew: newObj}))
	})
}
