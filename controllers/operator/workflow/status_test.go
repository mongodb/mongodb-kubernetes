package workflow

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"golang.org/x/xerrors"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

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
