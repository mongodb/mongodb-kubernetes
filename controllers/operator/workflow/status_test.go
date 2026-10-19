package workflow

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"golang.org/x/xerrors"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

func TestOnErrorPrepend(t *testing.T) {
	result := Pending("my message")
	decoratedResult := result.OnErrorPrepend("some prefix").(*pendingStatus)
	assert.Equal(t, "some prefix my message", decoratedResult.msg)

	failedResult := Failed(xerrors.Errorf("my failed result"))
	failedDecoratedResult := failedResult.OnErrorPrepend("failed wrapper").(*failedStatus)
	assert.Equal(t, "failed wrapper my failed result", failedDecoratedResult.msg)

	failedValidationResult := Invalid("my failed validation")
	failedDecoratedValidationResult := failedValidationResult.OnErrorPrepend("failed wrapper").(*invalidStatus)
	assert.Equal(t, "failed wrapper my failed validation", failedDecoratedValidationResult.msg)
}

func TestDisabledReconcileResult(t *testing.T) {
	disabledResult, _ := Disabled().ReconcileResult()
	assert.Equal(t, reconcile.Result{RequeueAfter: util.TWENTY_FOUR_HOURS}, disabledResult)
}

func TestFailedMerge_KeepsOptions(t *testing.T) {
	first := Failed(xerrors.New("first")).WithAdditionalOptions(status.NewPVCsStatusOption(&status.PVC{StatefulsetName: "a"}))
	merged := first.Merge(Failed(xerrors.New("second")))
	assert.True(t, ContainsPVCOption(merged.StatusOptions()))
}

func TestFailedWithAdditionalOptions_Appends(t *testing.T) {
	st := Failed(xerrors.New("x")).
		WithAdditionalOptions(status.NewPVCsStatusOption(&status.PVC{StatefulsetName: "a"})).
		WithAdditionalOptions(status.NewWarningsOption(nil))
	assert.True(t, ContainsPVCOption(st.StatusOptions()))
}
