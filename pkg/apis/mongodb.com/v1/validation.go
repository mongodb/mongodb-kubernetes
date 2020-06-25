package v1

import (
	"errors"
	"fmt"
	"strings"
)

type validationLevel int

const (
	SuccessLevel validationLevel = iota
	WarningLevel
	ErrorLevel
)

type ValidationResult struct {
	Msg   string
	Level validationLevel
}

func ValidationSuccess() ValidationResult {
	return ValidationResult{Level: SuccessLevel}
}

func ValidationWarning(msg string, params ...interface{}) ValidationResult {
	return ValidationResult{Msg: fmt.Sprintf(msg, params...), Level: WarningLevel}
}

func ValidationError(msg string, params ...interface{}) ValidationResult {
	return ValidationResult{Msg: fmt.Sprintf(msg, params...), Level: ErrorLevel}
}

func BuildValidationFailure(results []ValidationResult) error {
	var errorMsg []string
	if len(results) == 1 {
		return errors.New(results[0].Msg)
	}
	for _, err := range results {
		errorMsg = append(errorMsg, err.Msg)
	}
	return errors.New(strings.Join(errorMsg[:], ","))
}
