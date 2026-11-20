package operatoridentity

import (
	"testing"

	"github.com/stretchr/testify/assert"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	toolscache "k8s.io/client-go/tools/cache"

	"github.com/mongodb/mongodb-kubernetes/pkg/resourcenames"
)

const testNamespace = "test-ns"

func identityConfigMap(clusterName string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: resourcenames.MemberClusterIdentityConfigMapName, Namespace: testNamespace},
		Data:       map[string]string{resourcenames.MemberClusterIdentityClusterNameKey: clusterName},
	}
}

// cancelledWatcher returns a Watcher with the given boot identity and a channel that
// receives when the watcher's cancel func is invoked.
func cancelledWatcher(bootIdentity string) (*Watcher, chan struct{}) {
	cancelled := make(chan struct{}, 1)
	w := &Watcher{operatorNamespace: testNamespace, bootIdentity: bootIdentity, cancel: func() { cancelled <- struct{}{} }}
	return w, cancelled
}

func TestWatcher_Add(t *testing.T) {
	for _, tc := range []struct {
		name         string
		bootIdentity string
		clusterName  string
		expectCancel bool
	}{
		{
			name:         "add matching the boot identity does not restart (initial sync replay)",
			bootIdentity: "cluster-a",
			clusterName:  "cluster-a",
			expectCancel: false,
		},
		{
			name:         "add with a new identity restarts",
			bootIdentity: "",
			clusterName:  "cluster-a",
			expectCancel: true,
		},
		{
			name:         "add with a changed identity restarts",
			bootIdentity: "cluster-b",
			clusterName:  "cluster-a",
			expectCancel: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, cancelled := cancelledWatcher(tc.bootIdentity)
			handler := w.newEventHandler()

			handler.AddFunc(identityConfigMap(tc.clusterName))

			if tc.expectCancel {
				assert.Len(t, cancelled, 1)
			} else {
				assert.Empty(t, cancelled)
			}
		})
	}
}

func TestWatcher_Update(t *testing.T) {
	for _, tc := range []struct {
		name         string
		bootIdentity string
		newValue     string
		expectCancel bool
	}{
		{
			name:         "unchanged value does not restart",
			bootIdentity: "cluster-a",
			newValue:     "cluster-a",
			expectCancel: false,
		},
		{
			name:         "changed value restarts",
			bootIdentity: "cluster-a",
			newValue:     "cluster-b",
			expectCancel: true,
		},
		{
			name:         "emptied value restarts",
			bootIdentity: "cluster-a",
			newValue:     "",
			expectCancel: true,
		},
		{
			name:         "empty value matching a boot-time no-identity does not restart",
			bootIdentity: "",
			newValue:     "",
			expectCancel: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, cancelled := cancelledWatcher(tc.bootIdentity)
			handler := w.newEventHandler()

			handler.UpdateFunc(identityConfigMap(tc.bootIdentity), identityConfigMap(tc.newValue))

			if tc.expectCancel {
				assert.Len(t, cancelled, 1)
			} else {
				assert.Empty(t, cancelled)
			}
		})
	}
}

func TestWatcher_Delete(t *testing.T) {
	t.Run("delete with a boot identity restarts", func(t *testing.T) {
		w, cancelled := cancelledWatcher("cluster-a")
		handler := w.newEventHandler()

		handler.DeleteFunc(identityConfigMap("cluster-a"))

		assert.Len(t, cancelled, 1)
	})

	t.Run("delete with no boot identity does not restart", func(t *testing.T) {
		w, cancelled := cancelledWatcher("")
		handler := w.newEventHandler()

		handler.DeleteFunc(identityConfigMap("cluster-a"))

		assert.Empty(t, cancelled)
	})

	t.Run("delete tombstone with a boot identity restarts", func(t *testing.T) {
		w, cancelled := cancelledWatcher("cluster-a")
		handler := w.newEventHandler()

		handler.DeleteFunc(toolscache.DeletedFinalStateUnknown{Key: testNamespace + "/" + resourcenames.MemberClusterIdentityConfigMapName, Obj: identityConfigMap("cluster-a")})

		assert.Len(t, cancelled, 1)
	})

	t.Run("delete tombstone that cannot be resolved to the identity ConfigMap does not restart", func(t *testing.T) {
		w, cancelled := cancelledWatcher("cluster-a")
		handler := w.newEventHandler()

		handler.DeleteFunc(toolscache.DeletedFinalStateUnknown{Key: "gone", Obj: "not-a-configmap"})
		handler.DeleteFunc(toolscache.DeletedFinalStateUnknown{Key: "gone", Obj: &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: testNamespace}}})

		assert.Empty(t, cancelled)
	})
}

func TestWatcher_IgnoresOtherConfigMaps(t *testing.T) {
	otherCMs := map[string]*corev1.ConfigMap{
		"different name in the operator namespace": {
			ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: testNamespace},
			Data:       map[string]string{resourcenames.MemberClusterIdentityClusterNameKey: "cluster-b"},
		},
		"identity name in a different namespace": {
			ObjectMeta: metav1.ObjectMeta{Name: resourcenames.MemberClusterIdentityConfigMapName, Namespace: "other-ns"},
			Data:       map[string]string{resourcenames.MemberClusterIdentityClusterNameKey: "cluster-b"},
		},
	}
	for name, cm := range otherCMs {
		t.Run(name, func(t *testing.T) {
			w, cancelled := cancelledWatcher("cluster-a")
			handler := w.newEventHandler()

			handler.AddFunc(cm)
			handler.UpdateFunc(cm, cm)
			handler.DeleteFunc(cm)
			handler.DeleteFunc(toolscache.DeletedFinalStateUnknown{Key: cm.Namespace + "/" + cm.Name, Obj: cm})

			assert.Empty(t, cancelled)
		})
	}
}

func TestWatcher_MissingOrEmptyClusterNameKey(t *testing.T) {
	for _, tc := range []struct {
		name string
		cm   *corev1.ConfigMap
	}{
		{
			name: "missing clusterName key means no identity",
			cm: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: resourcenames.MemberClusterIdentityConfigMapName, Namespace: testNamespace},
				Data:       map[string]string{"other": "x"},
			},
		},
		{
			name: "nil data means no identity",
			cm: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: resourcenames.MemberClusterIdentityConfigMapName, Namespace: testNamespace},
			},
		},
		{
			name: "empty clusterName value means no identity",
			cm:   identityConfigMap(""),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, cancelled := cancelledWatcher("")
			handler := w.newEventHandler()

			handler.AddFunc(tc.cm)

			assert.Empty(t, cancelled, "an effective identity of \"\" matching the boot identity must not restart")
		})
	}
}

func TestWatcher_RestartsOnlyOnce(t *testing.T) {
	w, cancelled := cancelledWatcher("cluster-a")
	handler := w.newEventHandler()

	handler.UpdateFunc(identityConfigMap("cluster-a"), identityConfigMap("cluster-b"))
	handler.DeleteFunc(identityConfigMap("cluster-b"))

	assert.Len(t, cancelled, 1)
}
