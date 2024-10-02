package workflow

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"golang.org/x/xerrors"
)

func TestOnErrorPrepend(t *testing.T) {
	result := Pending("my message")
	decoratedResult := result.OnErrorPrepend("some prefix").(pendingStatus)
	assert.Equal(t, "some prefix my message", decoratedResult.msg)

	failedResult := Failed(xerrors.Errorf("my failed result"))
	failedDecoratedResult := failedResult.OnErrorPrepend("failed wrapper").(failedStatus)
	assert.Equal(t, "failed wrapper my failed result", failedDecoratedResult.msg)

	failedValidationResult := Invalid("my failed validation")
	failedDecoratedValidationResult := failedValidationResult.OnErrorPrepend("failed wrapper").(invalidStatus)
	assert.Equal(t, "failed wrapper my failed validation", failedDecoratedValidationResult.msg)
}
