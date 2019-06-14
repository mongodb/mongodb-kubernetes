package operator

import (
	"testing"

	v1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

func TestSettingUserStatus_ToPending_IsFilteredOut(t *testing.T) {
	userInUpdatedPhase := &v1.MongoDBUser{ObjectMeta: metav1.ObjectMeta{Name: "mms-user", Namespace: TestNamespace}, Status: v1.MongoDBUserStatus{Phase: v1.PhaseUpdated}}
	userInPendingPhase := &v1.MongoDBUser{ObjectMeta: metav1.ObjectMeta{Name: "mms-user", Namespace: TestNamespace}, Status: v1.MongoDBUserStatus{Phase: v1.PhasePending}}

	predicates := predicatesForUser()
	updateEvent := event.UpdateEvent{
		ObjectOld: userInUpdatedPhase,
		ObjectNew: userInPendingPhase,
	}
	assert.False(t, predicates.UpdateFunc(updateEvent), "changing phase from updated to pending should be filtered out")
}
