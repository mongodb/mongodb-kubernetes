package search

import (
	"errors"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"

	v1 "github.com/mongodb/mongodb-kubernetes/api/v1"
)

type namingStandard int

const (
	// dnsLabel: RFC 1123 DNS Label (max 63 chars, no dots) - StatefulSet, Service, Pod
	dnsLabel namingStandard = iota
	// dnsSubdomain: RFC 1123 DNS Subdomain (max 253 chars, dots allowed) - ConfigMap, Secret
	dnsSubdomain
)

type shardResourceName struct {
	ResourceType string
	Name         string
	Standard     namingStandard
}


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
		validateShardNames,
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

// generateShardResourceNames returns all resource names that will be created for a shard.
// Uses existing naming methods from MongoDBSearch to ensure consistency with actual resource creation.
func generateShardResourceNames(s *MongoDBSearch, shardName string) []shardResourceName {
	stsName := s.MongotStatefulSetForShard(shardName).Name
	resources := []shardResourceName{
		{ResourceType: "StatefulSet", Name: stsName, Standard: dnsLabel},
		{ResourceType: "Pod (max ordinal)", Name: stsName + "-999", Standard: dnsLabel},
		{ResourceType: "Service", Name: s.MongotServiceForShard(shardName).Name, Standard: dnsLabel},
		{ResourceType: "ConfigMap", Name: s.MongotConfigMapForShard(shardName).Name, Standard: dnsSubdomain},
	}

	if s.IsTLSConfigured() {
		resources = append(resources, shardResourceName{
			ResourceType: "TLS Certificate Secret",
			Name:         s.TLSSecretForShard(shardName).Name,
			Standard:     dnsSubdomain,
		})
	}

	if s.IsLBModeManaged() {
		resources = append(resources, shardResourceName{
			ResourceType: "Proxy Service",
			Name:         s.LoadBalancerProxyServiceNameForShard(shardName),
			Standard:     dnsLabel,
		})

		if s.IsTLSConfigured() {
			resources = append(resources, shardResourceName{
				ResourceType: "LB Server Certificate Secret",
				Name:         s.LoadBalancerServerCertForShard(shardName).Name,
				Standard:     dnsSubdomain,
			})
		}
	}

	return resources
}

func validateShardNames(s *MongoDBSearch) v1.ValidationResult {
	if !s.IsExternalSourceSharded() {
		return v1.ValidationSuccess()
	}

	shards := s.Spec.Source.ExternalMongoDBSource.ShardedCluster.Shards
	seenShardNames := make(map[string]struct{}, len(shards))

	for i, shard := range shards {
		shardName := shard.ShardName

		if shardName == "" {
			return v1.ValidationError("spec.source.external.shardedCluster.shards[%d].shardName is required", i)
		}

		if err := ValidateShardNameRFC1123(shardName); err != nil {
			return v1.ValidationError("%s", err.Error())
		}

		if _, exists := seenShardNames[shardName]; exists {
			return v1.ValidationError(
				"duplicate shardName '%s' in spec.source.external.shardedCluster.shards",
				shardName,
			)
		}
		seenShardNames[shardName] = struct{}{}

		resourceNames := generateShardResourceNames(s, shardName)
		for _, resource := range resourceNames {
			if err := validateResourceName(resource, s.Name, shardName); err != nil {
				return v1.ValidationError("%s", err.Error())
			}
		}
	}

	return v1.ValidationSuccess()
}

func validateResourceName(resource shardResourceName, searchName, shardName string) error {
	var maxLen int
	var standardName string

	switch resource.Standard {
	case dnsLabel:
		maxLen = validation.DNS1123LabelMaxLength
		standardName = "DNS label"
	case dnsSubdomain:
		maxLen = validation.DNS1123SubdomainMaxLength
		standardName = "DNS subdomain"
	}

	if len(resource.Name) > maxLen {
		excess := len(resource.Name) - maxLen
		return fmt.Errorf(
			"%s name '%s' (%d chars) exceeds the %d-character Kubernetes limit by %d characters. "+
				"Reduce MongoDBSearch name '%s' (%d chars) or shardName '%s' (%d chars)",
			resource.ResourceType, resource.Name, len(resource.Name), maxLen, excess,
			searchName, len(searchName), shardName, len(shardName),
		)
	}

	var validationErrs []string
	switch resource.Standard {
	case dnsLabel:
		validationErrs = validation.IsDNS1123Label(resource.Name)
	case dnsSubdomain:
		validationErrs = validation.IsDNS1123Subdomain(resource.Name)
	}

	if len(validationErrs) > 0 {
		return fmt.Errorf(
			"%s name '%s' is not a valid %s: %s",
			resource.ResourceType, resource.Name, standardName, strings.Join(validationErrs, ", "),
		)
	}

	return nil
}

func ValidateShardNameRFC1123(shardName string) error {
	if shardName == "" {
		return fmt.Errorf("shardName is required")
	}

	if errs := validation.IsDNS1123Label(shardName); len(errs) > 0 {
		return fmt.Errorf("shardName '%s' is invalid: %s", shardName, strings.Join(errs, ", "))
	}

	return nil
}
