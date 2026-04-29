package search

import (
	"errors"
	"fmt"
	"regexp"
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
		validateManagedLBExternalHostname,
		validateEndpointTemplate,
		validateRSEndpointTemplate,
		validateShardNames,
		validateJVMFlags,
		validateX509AuthConfig,
		validateClustersUniqueClusterName,
		validateClustersSyncSourceSelector,
		validateClustersShardOverrides,
		validateMCExternalHostnamePlaceholders,
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

	// Exactly one of managed or unmanaged must be set.
	// The XValidation marker on LoadBalancerConfig enforces this at the CRD level;
	// this check provides the same guarantee at the controller level.
	if s.Spec.LoadBalancer.Managed == nil && s.Spec.LoadBalancer.Unmanaged == nil {
		return v1.ValidationError("spec.loadBalancer must have exactly one of 'managed' or 'unmanaged' set")
	}
	if s.Spec.LoadBalancer.Managed != nil && s.Spec.LoadBalancer.Unmanaged != nil {
		return v1.ValidationError("spec.loadBalancer.managed and spec.loadBalancer.unmanaged are mutually exclusive")
	}

	return v1.ValidationSuccess()
}

// validateUnmanagedLBConfig validates that an endpoint is specified when unmanaged LB is configured.
func validateUnmanagedLBConfig(s *MongoDBSearch) v1.ValidationResult {
	if s.Spec.LoadBalancer == nil || s.Spec.LoadBalancer.Unmanaged == nil {
		return v1.ValidationSuccess()
	}

	if s.Spec.LoadBalancer.Unmanaged.Endpoint == "" {
		return v1.ValidationError("spec.loadBalancer.unmanaged.endpoint must be specified when spec.loadBalancer.unmanaged is configured")
	}

	return v1.ValidationSuccess()
}

// validateManagedLBExternalHostname validates that externalHostname is set when using managed LB
// with an external MongoDB source (Rule 5: Envoy needs the hostname for SNI matching).
func validateManagedLBExternalHostname(s *MongoDBSearch) v1.ValidationResult {
	if s.Spec.LoadBalancer == nil || s.Spec.LoadBalancer.Managed == nil {
		return v1.ValidationSuccess()
	}

	if s.IsExternalMongoDBSource() && s.Spec.LoadBalancer.Managed.ExternalHostname == "" {
		return v1.ValidationError("spec.loadBalancer.managed.externalHostname must be specified when using managed load balancer with an external MongoDB source")
	}

	return v1.ValidationSuccess()
}

// validateEndpointTemplate validates the unmanaged endpoint template format for sharded clusters.
func validateEndpointTemplate(s *MongoDBSearch) v1.ValidationResult {
	if !s.HasEndpointTemplate() {
		return v1.ValidationSuccess()
	}

	endpoint := s.Spec.LoadBalancer.Unmanaged.Endpoint

	count := strings.Count(endpoint, ShardNamePlaceholder)
	if count == 0 {
		return v1.ValidationError("spec.loadBalancer.unmanaged.endpoint template must contain at least one %s placeholder to differentiate between shards, found %d", ShardNamePlaceholder, count)
	}

	// Endpoint must contain more than just the placeholder(s)
	stripped := strings.TrimSpace(strings.ReplaceAll(endpoint, ShardNamePlaceholder, ""))
	if stripped == "" {
		return v1.ValidationError("spec.loadBalancer.unmanaged.endpoint must contain more than just the %s placeholder", ShardNamePlaceholder)
	}

	return v1.ValidationSuccess()
}

// validateRSEndpointTemplate validates that a ReplicaSet unmanaged endpoint does not contain a
// {shardName} template placeholder (Rule 8: template makes no sense for a ReplicaSet).
func validateRSEndpointTemplate(s *MongoDBSearch) v1.ValidationResult {
	if !s.IsLBModeUnmanaged() || s.IsExternalSourceSharded() || !s.IsExternalMongoDBSource() {
		return v1.ValidationSuccess()
	}

	if s.HasEndpointTemplate() {
		return v1.ValidationError("spec.loadBalancer.unmanaged.endpoint must not contain a %s placeholder for ReplicaSet deployments", ShardNamePlaceholder)
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

	if !s.IsShardedUnmanagedLB() {
		resources = append(resources, shardResourceName{
			ResourceType: "Proxy Service",
			Name:         s.ProxyServiceNameForShard(shardName).Name,
			Standard:     dnsLabel,
		})
	}

	if s.IsLBModeManaged() {
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

// validateJVMFlags validates the JVM flags provided by the users using MongoDBSearch's
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

// validateX509AuthConfig validates that x509 authentication is not configured alongside password authentication.
func validateX509AuthConfig(s *MongoDBSearch) v1.ValidationResult {
	if s.Spec.Source == nil || s.Spec.Source.X509 == nil {
		return v1.ValidationSuccess()
	}

	if s.Spec.Source.X509.ClientCertificateSecret.Name == "" {
		return v1.ValidationError("spec.source.x509.clientCertificateSecretRef.name must not be empty")
	}

	if s.Spec.Source.PasswordSecretRef != nil {
		return v1.ValidationError("x509 and password authentication are mutually exclusive: spec.source.x509 and spec.source.passwordSecretRef cannot both be set")
	}

	if s.Spec.Source.Username != nil {
		return v1.ValidationError("x509 and password authentication are mutually exclusive: spec.source.x509 and spec.source.username cannot both be set")
	}

	return v1.ValidationSuccess()
}

// validateClustersUniqueClusterName enforces clusterName uniqueness inside spec.clusters.
// ClusterName presence and immutability when len(clusters) > 1 are B13 scope.
func validateClustersUniqueClusterName(s *MongoDBSearch) v1.ValidationResult {
	if s.Spec.Clusters == nil {
		return v1.ValidationSuccess()
	}
	seen := make(map[string]int, len(*s.Spec.Clusters))
	for i, c := range *s.Spec.Clusters {
		if first, dup := seen[c.ClusterName]; dup {
			return v1.ValidationError(
				"duplicate clusterName %q in spec.clusters (entries %d and %d)",
				c.ClusterName, first, i,
			)
		}
		seen[c.ClusterName] = i
	}
	return v1.ValidationSuccess()
}

// validateClustersSyncSourceSelector enforces the at-most-one matchTags/hosts rule
// for every entry in spec.clusters. The "exactly one when len(clusters) > 1" rule
// lives in B13 (it depends on cluster-count semantics that aren't B14's scope).
func validateClustersSyncSourceSelector(s *MongoDBSearch) v1.ValidationResult {
	if s.Spec.Clusters == nil {
		return v1.ValidationSuccess()
	}
	for i, c := range *s.Spec.Clusters {
		sel := c.SyncSourceSelector
		if sel == nil {
			continue
		}
		if len(sel.MatchTags) > 0 && len(sel.Hosts) > 0 {
			return v1.ValidationError(
				"spec.clusters[%d].syncSourceSelector: matchTags and hosts are mutually exclusive",
				i,
			)
		}
	}
	return v1.ValidationSuccess()
}

// validateClustersShardOverrides enforces shardNames non-empty per ShardOverride.
// Whether shardOverrides[] is allowed at all (only sharded sources) is a B13
// source-aware rule and lives outside B14.
func validateClustersShardOverrides(s *MongoDBSearch) v1.ValidationResult {
	if s.Spec.Clusters == nil {
		return v1.ValidationSuccess()
	}
	for i, c := range *s.Spec.Clusters {
		for j, ov := range c.ShardOverrides {
			if len(ov.ShardNames) == 0 {
				return v1.ValidationError(
					"spec.clusters[%d].shardOverrides[%d].shardNames must have at least one entry",
					i, j,
				)
			}
		}
	}
	return v1.ValidationSuccess()
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

// validateMCExternalHostnamePlaceholders enforces:
//   - When len(spec.clusters) > 1 and managed LB is in use, externalHostname
//     must contain {clusterName} or {clusterIndex} so each cluster's resolved
//     hostname is distinct.
//   - When the source is external sharded AND len(spec.clusters) > 1,
//     externalHostname must additionally contain {shardName}.
//
// Single-cluster (len <= 1) and legacy specs (clusters nil) fall through —
// the existing single-cluster behaviour is preserved.
func validateMCExternalHostnamePlaceholders(s *MongoDBSearch) v1.ValidationResult {
	if !s.IsLBModeManaged() || s.Spec.LoadBalancer.Managed.ExternalHostname == "" {
		return v1.ValidationSuccess()
	}
	if s.Spec.Clusters == nil || len(*s.Spec.Clusters) <= 1 {
		return v1.ValidationSuccess()
	}
	tmpl := s.Spec.LoadBalancer.Managed.ExternalHostname
	hasCluster := strings.Contains(tmpl, ClusterNamePlaceholder) || strings.Contains(tmpl, ClusterIndexPlaceholder)
	if !hasCluster {
		return v1.ValidationError(
			"spec.loadBalancer.managed.externalHostname must contain %s or %s when len(spec.clusters) > 1",
			ClusterNamePlaceholder, ClusterIndexPlaceholder,
		)
	}
	if s.IsExternalSourceSharded() && !strings.Contains(tmpl, ShardNamePlaceholder) {
		return v1.ValidationError(
			"spec.loadBalancer.managed.externalHostname must contain %s for multi-cluster sharded deployments",
			ShardNamePlaceholder,
		)
	}
	return v1.ValidationSuccess()
}
