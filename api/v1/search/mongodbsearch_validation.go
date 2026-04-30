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

// ValidateUpdate validates a spec update against the previous object, returning an error on violation.
func (s *MongoDBSearch) ValidateUpdate(old *MongoDBSearch) error {
	for _, res := range s.RunUpdateValidations(old) {
		if res.Level == v1.ErrorLevel {
			return errors.New(res.Msg)
		}
	}
	return nil
}

func (s *MongoDBSearch) RunValidations() []v1.ValidationResult {
	validators := []func(*MongoDBSearch) v1.ValidationResult{
		validateClustersNotEmpty,
		validateClusterNames,
		validateClusterReplicas,
		validateSourceMutualExclusion,
		validateSourceNamespace,
		validateMultiClusterLBRequirements,
		validateReplicasRequireLB,
		validateMCExternalHostnamePlaceholders,
		validateShardedManagedLBHostnamePlaceholder,
		validateSyncSourceSelectorHostsMC,
		validateLBConfig,
		validateUnmanagedLBConfig,
		validateManagedLBExternalHostname,
		validateEndpointTemplate,
		validateRSEndpointTemplate,
		validateShardNames,
		validateJVMFlags,
		validateX509AuthConfig,
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

// RunUpdateValidations returns validation results that require comparing the old and new objects.
func (s *MongoDBSearch) RunUpdateValidations(old *MongoDBSearch) []v1.ValidationResult {
	validators := []func(newSearch, oldSearch *MongoDBSearch) v1.ValidationResult{
		validateClusterNamesImmutable,
	}

	var results []v1.ValidationResult
	for _, validator := range validators {
		res := validator(s, old)
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
	stsName := s.MongotStatefulSetForShard(0, shardName).Name
	resources := []shardResourceName{
		{ResourceType: "StatefulSet", Name: stsName, Standard: dnsLabel},
		{ResourceType: "Pod (max ordinal)", Name: stsName + "-999", Standard: dnsLabel},
		{ResourceType: "Service", Name: s.MongotServiceForShard(0, shardName).Name, Standard: dnsLabel},
		{ResourceType: "ConfigMap", Name: s.MongotConfigMapForShard(0, shardName).Name, Standard: dnsSubdomain},
	}

	if s.IsTLSConfigured() {
		resources = append(resources, shardResourceName{
			ResourceType: "TLS Certificate Secret",
			Name:         s.TLSSecretForShard(0, shardName).Name,
			Standard:     dnsSubdomain,
		})
	}

	if !s.IsShardedUnmanagedLB() {
		resources = append(resources, shardResourceName{
			ResourceType: "Proxy Service",
			Name:         s.ProxyServiceNameForShard(0, shardName).Name,
			Standard:     dnsLabel,
		})
	}

	if s.IsLBModeManaged() {
		if s.IsTLSConfigured() {
			resources = append(resources, shardResourceName{
				ResourceType: "LB Server Certificate Secret",
				Name:         s.LoadBalancerServerCertForShard(0, shardName).Name,
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

func ValidateShardNameRFC1123(shardName string) error {
	if shardName == "" {
		return fmt.Errorf("shardName is required")
	}

	if errs := validation.IsDNS1123Label(shardName); len(errs) > 0 {
		return fmt.Errorf("shardName '%s' is invalid: %s", shardName, strings.Join(errs, ", "))
	}

	return nil
}

// ClusterNamePlaceholder is used in managed LB externalHostname to refer to the cluster name.
const ClusterNamePlaceholder = "{clusterName}"

// ClusterIndexPlaceholder is used in managed LB externalHostname to refer to the cluster index.
const ClusterIndexPlaceholder = "{clusterIndex}"

// validateClustersNotEmpty rejects CRs where spec.clusters is absent or empty.
// This is the admission-time complement to the P1.7 runtime no-op path: newly created
// CRs must have at least one cluster entry; pre-upgrade CRs with empty clusters are
// caught at runtime by the reconcile gate.
func validateClustersNotEmpty(s *MongoDBSearch) v1.ValidationResult {
	if len(s.Spec.Clusters) == 0 {
		return v1.ValidationError("spec.clusters must have at least one entry")
	}
	return v1.ValidationSuccess()
}

// validateClusterNames validates that cluster names are unique within spec.clusters,
// and that every entry has a non-empty clusterName when len(spec.clusters) > 1.
func validateClusterNames(s *MongoDBSearch) v1.ValidationResult {
	if len(s.Spec.Clusters) <= 1 {
		return v1.ValidationSuccess()
	}
	seen := make(map[string]struct{}, len(s.Spec.Clusters))
	for i, c := range s.Spec.Clusters {
		if c.ClusterName == "" {
			return v1.ValidationError("spec.clusters[%d].clusterName is required when len(spec.clusters) > 1", i)
		}
		if _, exists := seen[c.ClusterName]; exists {
			return v1.ValidationError("duplicate clusterName '%s' in spec.clusters", c.ClusterName)
		}
		seen[c.ClusterName] = struct{}{}
	}
	return v1.ValidationSuccess()
}

// validateClusterReplicas rejects any cluster entry with replicas == 0.
func validateClusterReplicas(s *MongoDBSearch) v1.ValidationResult {
	for i, c := range s.Spec.Clusters {
		if c.Replicas < 1 {
			return v1.ValidationError("spec.clusters[%d].replicas must be >= 1", i)
		}
	}
	return v1.ValidationSuccess()
}

// validateSourceMutualExclusion rejects CRs that set both mongodbResourceRef and external source.
func validateSourceMutualExclusion(s *MongoDBSearch) v1.ValidationResult {
	if s.Spec.Source == nil {
		return v1.ValidationSuccess()
	}
	if s.Spec.Source.MongoDBResourceRef != nil && s.Spec.Source.ExternalMongoDBSource != nil {
		return v1.ValidationError("spec.source.mongodbResourceRef and spec.source.external are mutually exclusive")
	}
	return v1.ValidationSuccess()
}

// validateSourceNamespace rejects CRs where spec.source.mongodbResourceRef.namespace is set
// to a value other than the MongoDBSearch namespace. Cross-namespace source references are
// not supported; the field is reserved for future use only (TD §11.8.1 "same namespace").
func validateSourceNamespace(s *MongoDBSearch) v1.ValidationResult {
	if s.Spec.Source == nil || s.Spec.Source.MongoDBResourceRef == nil {
		return v1.ValidationSuccess()
	}
	ns := s.Spec.Source.MongoDBResourceRef.Namespace
	if ns != "" && ns != s.Namespace {
		return v1.ValidationError(
			"spec.source.mongodbResourceRef.namespace must equal metadata.namespace (%s) when set; cross-namespace source references are not supported",
			s.Namespace,
		)
	}
	return v1.ValidationSuccess()
}

// validateShardedManagedLBHostnamePlaceholder requires that externalHostname contains
// {shardName} when the source is an external sharded cluster with more than one shard and
// managed LB is configured. Without the placeholder the operator cannot derive a distinct
// SNI hostname for each shard, which makes it impossible to route correctly.
func validateShardedManagedLBHostnamePlaceholder(s *MongoDBSearch) v1.ValidationResult {
	if !s.IsExternalSourceSharded() {
		return v1.ValidationSuccess()
	}
	if s.Spec.LoadBalancer == nil || s.Spec.LoadBalancer.Managed == nil {
		return v1.ValidationSuccess()
	}
	hostname := s.Spec.LoadBalancer.Managed.ExternalHostname
	if hostname == "" {
		return v1.ValidationSuccess()
	}
	shards := s.Spec.Source.ExternalMongoDBSource.ShardedCluster.Shards
	if len(shards) <= 1 {
		return v1.ValidationSuccess()
	}
	if !strings.Contains(hostname, ShardNamePlaceholder) {
		return v1.ValidationError(
			"spec.loadBalancer.managed.externalHostname must contain %s when the source has multiple shards so the operator can derive a per-shard endpoint",
			ShardNamePlaceholder,
		)
	}
	return v1.ValidationSuccess()
}

// validateMultiClusterLBRequirements enforces the GA LB posture for multi-cluster deployments:
//   - managed LB is required when len(spec.clusters) > 1
//   - unmanaged LB is forbidden when len(spec.clusters) > 1
func validateMultiClusterLBRequirements(s *MongoDBSearch) v1.ValidationResult {
	if len(s.Spec.Clusters) <= 1 {
		return v1.ValidationSuccess()
	}
	if s.Spec.LoadBalancer == nil || s.Spec.LoadBalancer.Managed == nil {
		return v1.ValidationError("spec.loadBalancer.managed is required when len(spec.clusters) > 1")
	}
	if s.Spec.LoadBalancer.Unmanaged != nil {
		return v1.ValidationError("spec.loadBalancer.unmanaged is forbidden when len(spec.clusters) > 1")
	}
	return v1.ValidationSuccess()
}

// validateReplicasRequireLB rejects CRs where any cluster has replicas > 1 but no LB is configured.
// A single mongot replica can be reached directly; more than one requires a load balancer so
// that mongod can address a single stable endpoint.
func validateReplicasRequireLB(s *MongoDBSearch) v1.ValidationResult {
	for i, c := range s.Spec.Clusters {
		if c.Replicas > 1 && s.Spec.LoadBalancer == nil {
			return v1.ValidationError("spec.clusters[%d].replicas > 1 requires a load balancer (spec.loadBalancer)", i)
		}
	}
	return v1.ValidationSuccess()
}

// validateMCExternalHostnamePlaceholders requires that externalHostname contains
// a {clusterName} or {clusterIndex} placeholder when len(spec.clusters) > 1 and
// managed LB is configured, so the operator can derive a per-cluster endpoint.
func validateMCExternalHostnamePlaceholders(s *MongoDBSearch) v1.ValidationResult {
	if len(s.Spec.Clusters) <= 1 {
		return v1.ValidationSuccess()
	}
	if s.Spec.LoadBalancer == nil || s.Spec.LoadBalancer.Managed == nil {
		return v1.ValidationSuccess()
	}
	hostname := s.Spec.LoadBalancer.Managed.ExternalHostname
	if hostname == "" {
		return v1.ValidationSuccess()
	}
	if !strings.Contains(hostname, ClusterNamePlaceholder) && !strings.Contains(hostname, ClusterIndexPlaceholder) {
		return v1.ValidationError(
			"spec.loadBalancer.managed.externalHostname must contain %s or %s when len(spec.clusters) > 1",
			ClusterNamePlaceholder, ClusterIndexPlaceholder,
		)
	}
	return v1.ValidationSuccess()
}

// validateSyncSourceSelectorHostsMC rejects spec.clusters[i].syncSourceSelector.hosts
// when len(spec.clusters) > 1. Multi-cluster requires tag-based routing via matchTags.
func validateSyncSourceSelectorHostsMC(s *MongoDBSearch) v1.ValidationResult {
	if len(s.Spec.Clusters) <= 1 {
		return v1.ValidationSuccess()
	}
	for i, c := range s.Spec.Clusters {
		if c.SyncSourceSelector != nil && len(c.SyncSourceSelector.Hosts) > 0 {
			return v1.ValidationError(
				"spec.clusters[%d].syncSourceSelector.hosts is forbidden when len(spec.clusters) > 1; use matchTags for multi-cluster routing",
				i,
			)
		}
	}
	return v1.ValidationSuccess()
}

// validateClusterNamesImmutable rejects updates that rename or remove an existing clusterName.
// Phase 1 is deliberately strict: any clusterName present in the old spec must remain present
// in the new spec. This protects the stable clusterIndex persisted in the StateStore ConfigMap —
// a renamed cluster would receive a fresh index, orphaning all resources (StatefulSet, Services,
// ConfigMaps) that were created under the old index. Users who want to replace a cluster should
// delete the MongoDBSearch and recreate it with the new clusterName.
func validateClusterNamesImmutable(newSearch, oldSearch *MongoDBSearch) v1.ValidationResult {
	oldNames := make(map[string]struct{}, len(oldSearch.Spec.Clusters))
	for _, c := range oldSearch.Spec.Clusters {
		if c.ClusterName != "" {
			oldNames[c.ClusterName] = struct{}{}
		}
	}
	if len(oldNames) == 0 {
		return v1.ValidationSuccess()
	}
	newNames := make(map[string]struct{}, len(newSearch.Spec.Clusters))
	for _, c := range newSearch.Spec.Clusters {
		if c.ClusterName != "" {
			newNames[c.ClusterName] = struct{}{}
		}
	}
	for name := range oldNames {
		if _, exists := newNames[name]; !exists {
			return v1.ValidationError(
				"clusterName '%s' cannot be removed or renamed; remove the cluster entry instead of renaming it",
				name,
			)
		}
	}
	return v1.ValidationSuccess()
}
