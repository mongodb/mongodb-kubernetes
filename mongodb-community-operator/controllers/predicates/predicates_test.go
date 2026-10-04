package predicates

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1"
)

func TestOnlyOnSpecChange(t *testing.T) {
	predicate := OnlyOnSpecChange()

	t.Run("returns false for metadata-only updates", func(t *testing.T) {
		oldResource := newMongoDBCommunityForPredicateTest()
		newResource := newMongoDBCommunityForPredicateTest()

		newResource.Annotations = map[string]string{
			"example.com/test": "value",
		}

		if predicate.Update(event.UpdateEvent{ObjectOld: oldResource, ObjectNew: newResource}) {
			t.Fatal("expected metadata-only update to be ignored")
		}
	})

	t.Run("returns true for spec updates", func(t *testing.T) {
		oldResource := newMongoDBCommunityForPredicateTest()
		newResource := newMongoDBCommunityForPredicateTest()

		newResource.Spec.Members = 4

		if !predicate.Update(event.UpdateEvent{ObjectOld: oldResource, ObjectNew: newResource}) {
			t.Fatal("expected spec update to trigger reconciliation")
		}
	})

	t.Run("returns true when deletion timestamp is set", func(t *testing.T) {
		oldResource := newMongoDBCommunityForPredicateTest()
		newResource := newMongoDBCommunityForPredicateTest()

		now := metav1.Now()
		newResource.DeletionTimestamp = &now

		if !predicate.Update(event.UpdateEvent{ObjectOld: oldResource, ObjectNew: newResource}) {
			t.Fatal("expected deletion timestamp update to trigger reconciliation")
		}
	})
}

func newMongoDBCommunityForPredicateTest() *mdbv1.MongoDBCommunity {
	return &mdbv1.MongoDBCommunity{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "mongodb",
			Namespace: "mongodb",
		},
		Spec: mdbv1.MongoDBCommunitySpec{
			Type:    mdbv1.ReplicaSet,
			Members: 3,
		},
	}
}
