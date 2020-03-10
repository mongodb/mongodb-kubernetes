package v1

import (
	"errors"
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

func validationSuccess() ValidationResult {
	return ValidationResult{Level: SuccessLevel}
}

func validationWarning(msg string) ValidationResult {
	return ValidationResult{Msg: msg, Level: WarningLevel}
}

func validationError(msg string) ValidationResult {
	return ValidationResult{Msg: msg, Level: ErrorLevel}
}

func buildValidationFailure(results []ValidationResult) error {
	var errorMsg []string
	if len(results) == 1 {
		return errors.New(results[0].Msg)
	}
	for _, err := range results {
		errorMsg = append(errorMsg, err.Msg)
	}
	return errors.New(strings.Join(errorMsg[:], ","))
}
