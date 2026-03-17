package search

import (
	"errors"
	"regexp"
	"strings"

	v1 "github.com/mongodb/mongodb-kubernetes/api/v1"
)

var validJVMFlagChars = regexp.MustCompile(`^[a-zA-Z0-9._+:=-]+$`)

func (s *MongoDBSearch) ValidateSpec() error {
	for _, res := range s.RunValidations() {
		if res.Level == v1.ErrorLevel {
			return errors.New(res.Msg)
		}
	}
	return nil
}

func (s *MongoDBSearch) RunValidations() []v1.ValidationResult {
	validators := []func(*MongoDBSearch) v1.ValidationResult{
		validateLBConfig,
		validateUnmanagedLBConfig,
		validateEndpointTemplate,
		validateJVMFlags,
	}

	var results []v1.ValidationResult
	for _, validator := range validators {
		res := validator(s)
		if res.Level > 0 {
			results = append(results, res)
		}
	}
	return results
}

func validateLBConfig(s *MongoDBSearch) v1.ValidationResult {
	if s.Spec.LoadBalancer == nil {
		// LB config is optional
		return v1.ValidationSuccess()
	}

	// Mode must be specified if LB config is present
	if s.Spec.LoadBalancer.Mode == "" {
		return v1.ValidationError("spec.lb.mode must be specified when spec.lb is configured")
	}

	// Mode must be either Managed or Unmanaged
	if s.Spec.LoadBalancer.Mode != LBModeManaged && s.Spec.LoadBalancer.Mode != LBModeUnmanaged {
		return v1.ValidationError("spec.lb.mode must be either 'Managed' or 'Unmanaged', got '%s'", s.Spec.LoadBalancer.Mode)
	}

	return v1.ValidationSuccess()
}

// validateUnmanagedLBConfig validates that an endpoint is specified when mode is Unmanaged
func validateUnmanagedLBConfig(s *MongoDBSearch) v1.ValidationResult {
	if s.Spec.LoadBalancer == nil || s.Spec.LoadBalancer.Mode != LBModeUnmanaged {
		return v1.ValidationSuccess()
	}

	if s.Spec.LoadBalancer.Endpoint == "" {
		return v1.ValidationError("spec.lb.endpoint must be specified when spec.lb.mode is 'Unmanaged'")
	}

	return v1.ValidationSuccess()
}

// validateEndpointTemplate validates the endpoint template format
func validateEndpointTemplate(s *MongoDBSearch) v1.ValidationResult {
	if !s.HasEndpointTemplate() {
		return v1.ValidationSuccess()
	}

	endpoint := s.Spec.LoadBalancer.Endpoint

	count := strings.Count(endpoint, ShardNamePlaceholder)
	if count == 0 {
		return v1.ValidationError("spec.lb.endpoint template must contain at least one %s placeholder to differentiate between shards, found %d", ShardNamePlaceholder, count)
	}

	// Endpoint must contain more than just the placeholder(s)
	stripped := strings.TrimSpace(strings.ReplaceAll(endpoint, ShardNamePlaceholder, ""))
	if stripped == "" {
		return v1.ValidationError("spec.lb.endpoint must contain more than just the %s placeholder", ShardNamePlaceholder)
	}

	return v1.ValidationSuccess()
}

// validateJVMFlags validates the JVM flags passed provided by the users using MongoDBSearch's
// spec.JVMFlags field.
// These jvm flags are directly passed to the mongot process that we run.
func validateJVMFlags(s *MongoDBSearch) v1.ValidationResult {
	for i, flag := range s.Spec.JVMFlags {
		if flag == "" {
			return v1.ValidationError("MongoDBSearch resource is invalid, spec.jvmFlags[%d] must not be empty", i)
		}

		if strings.Contains(flag, " ") {
			return v1.ValidationError("MongoDBSearch resource is invalid, spec.jvmFlags[%d] must not contain spaces, got '%s'", i, flag)
		}

		if !strings.HasPrefix(flag, "-X") && !strings.HasPrefix(flag, "-XX:") && !strings.HasPrefix(flag, "-D") {
			return v1.ValidationError("MongoDBSearch resource is invalid, spec.jvmFlags[%d] must start with -X, -XX:, or -D, got '%s'", i, flag)
		}

		if !validJVMFlagChars.MatchString(flag) {
			return v1.ValidationError("MongoDBSearch resource is invalid, spec.jvmFlags[%d] contains invalid characters, only [a-zA-Z0-9._+:-=] are allowed, got '%s'", i, flag)
		}
	}

	return v1.ValidationSuccess()
}
