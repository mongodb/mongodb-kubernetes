package membercluster

import (
	"testing"

	"github.com/stretchr/testify/assert"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	operatorv1 "github.com/mongodb/mongodb-kubernetes/api/operator/v1"
)

func TestWatcher_Add(t *testing.T) {
	for _, tc := range []struct {
		name         string
		synced       bool
		expectCancel bool
	}{
		{name: "creation after cache sync triggers restart", synced: true, expectCancel: true},
		{name: "creation during initial cache sync is ignored", synced: false, expectCancel: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cancelled := make(chan struct{}, 1)
			w := &Watcher{cancel: func() { cancelled <- struct{}{} }}
			w.synced.Store(tc.synced)
			handler := w.newEventHandler()
			handler.AddFunc(&operatorv1.MemberCluster{})

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
		oldGen       int64
		newGen       int64
		expectCancel bool
	}{
		{name: "generation changed (spec) triggers restart", oldGen: 1, newGen: 2, expectCancel: true},
		{name: "unchanged generation (e.g. status write) does not trigger restart", oldGen: 1, newGen: 1, expectCancel: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cancelled := make(chan struct{}, 1)
			w := &Watcher{cancel: func() { cancelled <- struct{}{} }}
			handler := w.newEventHandler()

			oldObj := &operatorv1.MemberCluster{ObjectMeta: metav1.ObjectMeta{Generation: tc.oldGen}}
			newObj := &operatorv1.MemberCluster{ObjectMeta: metav1.ObjectMeta{Generation: tc.newGen}}
			handler.UpdateFunc(oldObj, newObj)

			if tc.expectCancel {
				assert.Len(t, cancelled, 1)
			} else {
				assert.Empty(t, cancelled)
			}
		})
	}
}

func TestWatcher_Delete(t *testing.T) {
	cancelled := make(chan struct{}, 1)
	w := &Watcher{cancel: func() { cancelled <- struct{}{} }}
	handler := w.newEventHandler()
	handler.DeleteFunc(&operatorv1.MemberCluster{})
	assert.Len(t, cancelled, 1)
}
