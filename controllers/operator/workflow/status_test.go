package workflow

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/xerrors"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
	"github.com/mongodb/mongodb-kubernetes/controllers/om/apierror"
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

func TestAutomationConfigConflictIsPending(t *testing.T) {
	conflict := &apierror.Error{
		Status:    new(409),
		Reason:    "Conflict",
		ErrorCode: apierror.AutomationConfigConcurrentModification,
	}

	tests := []struct {
		name string
		err  error
		want status.Phase
	}{
		{
			name: "direct conflict",
			err:  conflict,
			want: status.PhasePending,
		},
		{
			name: "wrapped conflict",
			err:  xerrors.Errorf("failed to update automation config: %w", conflict),
			want: status.PhasePending,
		},
		{
			name: "other 409 stays failed",
			err: &apierror.Error{
				Status:    new(409),
				ErrorCode: "CANNOT_MODIFY_SHARD_BACKUP_CONFIG",
			},
			want: status.PhaseFailed,
		},
		{
			name: "other error stays failed",
			err:  xerrors.New("boom"),
			want: status.PhaseFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Failed(tt.err)
			assert.Equal(t, tt.want, got.Phase())

			result, err := got.ReconcileResult()
			require.NoError(t, err)
			assert.Equal(t, 10*time.Second, result.RequeueAfter)
		})
	}
}
