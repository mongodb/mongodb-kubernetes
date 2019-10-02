package operator

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestOnErrorPrepend(t *testing.T) {
	result := pending("my message")
	decoratedResult := result.onErrorPrepend("some prefix").(*pendingStatus)
	assert.Equal(t, "some prefix my message", decoratedResult.msg)

	okResult := ok()
	okDecoratedResult := okResult.onErrorPrepend("this message will not be added").(*successStatus)
	assert.Equal(t, "", okDecoratedResult.msg)

	failedResult := failed("my failed result")
	failedDecoratedResult := failedResult.onErrorPrepend("failed wrapper").(*errorStatus)
	assert.Equal(t, "failed wrapper my failed result", failedDecoratedResult.err.Error())

	failedValidationResult := failedValidation("my failed validation")
	failedDecoratedValidationResult := failedValidationResult.onErrorPrepend("failed wrapper").(*validationStatus)
	assert.Equal(t, "failed wrapper my failed validation", failedDecoratedValidationResult.err.Error())
}
