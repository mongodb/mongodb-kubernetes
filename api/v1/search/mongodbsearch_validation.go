package search

import (
	"errors"
	"fmt"
	"strings"

	v1 "github.com/mongodb/mongodb-kubernetes/api/v1"
)

// ValidateSpec validates the MongoDBSearch spec
func (s *MongoDBSearch) ValidateSpec() error {
	for _, res := range s.RunValidations() {
		if res.Level == v1.ErrorLevel {
			return errors.New(res.Msg)
		}
	}
	return nil
}

// RunValidations runs all validation rules and returns the results
func (s *MongoDBSearch) RunValidations() []v1.ValidationResult {
	validators := []func(*MongoDBSearch) v1.ValidationResult{
		validateLBConfig,
		validateExternalLBConfig,
		validateShardedExternalLBEndpoints,
		validateReplicasForExternalLB,
		validateEndpointTemplate,
		validateTLSConfig,
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

// validateLBConfig validates the load balancer configuration
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

// validateExternalLBConfig validates the external LB configuration
func validateExternalLBConfig(s *MongoDBSearch) v1.ValidationResult {
	if s.Spec.LoadBalancer == nil || s.Spec.LoadBalancer.Mode != LBModeUnmanaged {
		return v1.ValidationSuccess()
	}

	// External config should be present when mode is Unmanaged
	if s.Spec.LoadBalancer.External == nil {
		return v1.ValidationError("spec.lb.external must be specified when spec.lb.mode is 'Unmanaged'")
	}

	// Either endpoint or sharded.endpoints must be specified
	hasEndpoint := s.Spec.LoadBalancer.External.Endpoint != ""
	hasShardedEndpoints := s.Spec.LoadBalancer.External.Sharded != nil &&
		len(s.Spec.LoadBalancer.External.Sharded.Endpoints) > 0

	if !hasEndpoint && !hasShardedEndpoints {
		return v1.ValidationError("spec.lb.external must have either 'endpoint' or 'sharded.endpoints' specified")
	}

	return v1.ValidationSuccess()
}

// validateShardedExternalLBEndpoints validates the per-shard LB endpoints (legacy format)
func validateShardedExternalLBEndpoints(s *MongoDBSearch) v1.ValidationResult {
	if !s.IsShardedExternalLB() {
		return v1.ValidationSuccess()
	}

	// Skip validation for template format - it's validated by validateEndpointTemplate
	if s.HasEndpointTemplate() {
		return v1.ValidationSuccess()
	}

	// Legacy format validation
	if s.Spec.LoadBalancer.External.Sharded == nil {
		return v1.ValidationSuccess()
	}

	seenShardNames := make(map[string]bool)
	for i, endpoint := range s.Spec.LoadBalancer.External.Sharded.Endpoints {
		// ShardName must not be empty
		if endpoint.ShardName == "" {
			return v1.ValidationError("spec.lb.external.sharded.endpoints[%d].shardName must not be empty", i)
		}

		// Endpoint must not be empty
		if endpoint.Endpoint == "" {
			return v1.ValidationError("spec.lb.external.sharded.endpoints[%d].endpoint must not be empty for shard '%s'", i, endpoint.ShardName)
		}

		// ShardName must be unique
		if seenShardNames[endpoint.ShardName] {
			return v1.ValidationError("spec.lb.external.sharded.endpoints contains duplicate shardName '%s'", endpoint.ShardName)
		}
		seenShardNames[endpoint.ShardName] = true
	}

	return v1.ValidationSuccess()
}

// validateReplicasForExternalLB validates replicas configuration for sharded external LB.
// Multiple replicas per shard are supported when external LB endpoints are configured.
func validateReplicasForExternalLB(s *MongoDBSearch) v1.ValidationResult {
	// No validation needed - multiple replicas are supported with external LB
	return v1.ValidationSuccess()
}

// validateEndpointTemplate validates the endpoint template format
func validateEndpointTemplate(s *MongoDBSearch) v1.ValidationResult {
	if !s.HasEndpointTemplate() {
		return v1.ValidationSuccess()
	}

	endpoint := s.Spec.LoadBalancer.External.Endpoint

	// Template must contain exactly one {shardName} placeholder
	count := strings.Count(endpoint, ShardNamePlaceholder)
	if count != 1 {
		return v1.ValidationError("spec.lb.external.endpoint template must contain exactly one %s placeholder, found %d", ShardNamePlaceholder, count)
	}

	// Template should have some content before or after the placeholder
	if endpoint == ShardNamePlaceholder {
		return v1.ValidationError("spec.lb.external.endpoint template must contain more than just the %s placeholder", ShardNamePlaceholder)
	}

	return v1.ValidationSuccess()
}

// validateTLSConfig validates the TLS configuration
func validateTLSConfig(s *MongoDBSearch) v1.ValidationResult {
	if s.Spec.Security.TLS == nil {
		return v1.ValidationSuccess()
	}

	// TLS is valid in all cases:
	// 1. CertificateKeySecret.Name is specified - use explicit secret name
	// 2. CertsSecretPrefix is specified - use {prefix}-{resourceName}-search-cert
	// 3. Both are empty - use default {resourceName}-search-cert
	// No validation error needed as we always have a valid fallback

	return v1.ValidationSuccess()
}

// ValidateShardEndpointsForCluster validates that all shards in the cluster have corresponding LB endpoints
// This is called during reconciliation when we know the actual shard names from the MongoDB resource
func (s *MongoDBSearch) ValidateShardEndpointsForCluster(shardNames []string) error {
	if !s.IsShardedExternalLB() {
		return nil
	}

	// Template format automatically handles all shards - no validation needed
	if s.HasEndpointTemplate() {
		return nil
	}

	// Legacy format: validate that all shards have endpoints
	endpointMap := s.GetShardEndpointMap()

	var missingShards []string
	for _, shardName := range shardNames {
		if _, ok := endpointMap[shardName]; !ok {
			missingShards = append(missingShards, shardName)
		}
	}

	if len(missingShards) > 0 {
		return fmt.Errorf("missing LB endpoints for shards: %v. Configure spec.lb.external.sharded.endpoints for each shard or use endpoint template with %s", missingShards, ShardNamePlaceholder)
	}

	return nil
}
