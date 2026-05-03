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
		validateClustersClusterNameNonEmpty,
		validateClustersUniqueClusterName,
		validateClustersSyncSourceSelector,
		validateClustersShardOverrides,
		validateClustersAndTopLevelFieldsMutuallyExclusive,
		validateClustersEnvoyResourceNames,
		validateClustersNoRename,
		validateMCExternalHostnamePlaceholders,
		validateExternalHostnameDNSLength,
		validateMCRejectsUnmanagedLB,
		validateMCRequiresLoadBalancerManaged,
		validateMCMatchTagsNonEmpty,
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

// validateClustersClusterNameNonEmpty rejects an empty spec.clusters[i].clusterName
// when len(spec.clusters) > 1. The single-cluster degenerate case (len <= 1) keeps
// allowing an empty clusterName. Uniqueness is the next validator's job; the dedicated
// "is required" message fires here so a two-empty-names spec surfaces the actionable
// hint instead of "duplicate".
func validateClustersClusterNameNonEmpty(s *MongoDBSearch) v1.ValidationResult {
	if s.Spec.Clusters == nil || len(*s.Spec.Clusters) <= 1 {
		return v1.ValidationSuccess()
	}
	for i, c := range *s.Spec.Clusters {
		if c.ClusterName == "" {
			return v1.ValidationError(
				"spec.clusters[%d].clusterName is required when len(spec.clusters) > 1",
				i,
			)
		}
	}
	return v1.ValidationSuccess()
}

// validateClustersUniqueClusterName enforces clusterName uniqueness inside spec.clusters.
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
// for every entry in spec.clusters.
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

// validateClustersEnvoyResourceNames enforces DNS-1123 length and label/subdomain
// rules on the per-cluster Envoy Deployment + ConfigMap names that the
// Envoy reconciler will create. Without this admission check, an over-long
// clusterName would fail at runtime with a kube API error during reconcile.
//
// Mirrors the sharded-resource-name pattern in generateShardResourceNames /
// validateResourceName.
func validateClustersEnvoyResourceNames(s *MongoDBSearch) v1.ValidationResult {
	if s.Spec.Clusters == nil {
		return v1.ValidationSuccess()
	}
	for _, c := range *s.Spec.Clusters {
		if c.ClusterName == "" {
			continue
		}
		resources := []shardResourceName{
			{
				ResourceType: "Envoy Deployment (per cluster)",
				Name:         s.LoadBalancerDeploymentNameForCluster(c.ClusterName),
				Standard:     dnsLabel,
			},
			{
				ResourceType: "Envoy ConfigMap (per cluster)",
				Name:         s.LoadBalancerConfigMapNameForCluster(c.ClusterName),
				Standard:     dnsSubdomain,
			},
		}
		for _, resource := range resources {
			if err := validateResourceName(resource, s.Name, c.ClusterName); err != nil {
				return v1.ValidationError("%s", err.Error())
			}
		}
	}
	return v1.ValidationSuccess()
}

// validateClustersShardOverrides enforces shardNames non-empty per ShardOverride.
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

// validateClustersAndTopLevelFieldsMutuallyExclusive enforces the mutual-exclusion
// rule: when spec.clusters is set, none of the auto-promotion-eligible top-level
// distribution fields (spec.replicas, spec.resourceRequirements, spec.persistence,
// spec.statefulSet) may also be set. This keeps the migration path unambiguous —
// either the user is on the legacy single-cluster path (top-level only) or on
// the new per-cluster shape (spec.clusters only).
//
// jvmFlags and loadBalancer remain top-level + per-cluster combinable on purpose
// (top-level is the default that per-cluster overrides) and are intentionally
// excluded from this check.
func validateClustersAndTopLevelFieldsMutuallyExclusive(s *MongoDBSearch) v1.ValidationResult {
	if s.Spec.Clusters == nil {
		return v1.ValidationSuccess()
	}
	//nolint:staticcheck // SA1019: deprecated fields — this is the documented detection path.
	if s.Spec.Replicas != nil {
		return v1.ValidationError("spec.replicas and spec.clusters are mutually exclusive; specify replicas inside spec.clusters[].replicas instead")
	}
	//nolint:staticcheck // SA1019
	if s.Spec.ResourceRequirements != nil {
		return v1.ValidationError("spec.resourceRequirements and spec.clusters are mutually exclusive; specify resourceRequirements inside spec.clusters[].resourceRequirements instead")
	}
	//nolint:staticcheck // SA1019
	if s.Spec.Persistence != nil {
		return v1.ValidationError("spec.persistence and spec.clusters are mutually exclusive; specify persistence inside spec.clusters[].persistence instead")
	}
	//nolint:staticcheck // SA1019
	if s.Spec.StatefulSetConfiguration != nil {
		return v1.ValidationError("spec.statefulSet and spec.clusters are mutually exclusive; specify statefulSet inside spec.clusters[].statefulSet instead")
	}
	return v1.ValidationSuccess()
}

// validateClustersNoRename rejects an Update where a clusterName has been removed
// from spec.clusters AND a different clusterName has been added in the same update.
// Pure rename (one removed, one added) is the operation we forbid; pure remove or
// pure add is allowed (the index mapping handles both safely — removed indices are
// preserved, added clusters get the next monotonic index).
//
// This is a soft, single-update rule: a remove-then-readd in two separate updates
// is indistinguishable from a real rename. We accept that — the index is preserved
// in both cases, so the worst case is that a real rename slips through across two
// updates and the user sees no observable difference. There is no admission webhook
// for MongoDBSearch today; all the more reason to keep the rule tight enough to
// catch the obvious mistake but loose enough not to false-positive on benign edits.
func validateClustersNoRename(s *MongoDBSearch) v1.ValidationResult {
	raw, ok := s.Annotations[LastClusterNumMapping]
	if !ok || raw == "" {
		return v1.ValidationSuccess()
	}
	previous := parseClusterMapping(raw)
	if len(previous) == 0 {
		return v1.ValidationSuccess()
	}
	if s.Spec.Clusters == nil {
		return v1.ValidationSuccess()
	}
	currentSet := make(map[string]struct{}, len(*s.Spec.Clusters))
	for _, c := range *s.Spec.Clusters {
		currentSet[c.ClusterName] = struct{}{}
	}
	var removed []string
	for name := range previous {
		if _, ok := currentSet[name]; !ok {
			removed = append(removed, name)
		}
	}
	var added []string
	for name := range currentSet {
		if _, ok := previous[name]; !ok {
			added = append(added, name)
		}
	}
	if len(removed) > 0 && len(added) > 0 {
		return v1.ValidationError(
			"clusterName changes are not allowed: removed=%v added=%v. To rename a cluster, recreate the MongoDBSearch resource",
			removed, added,
		)
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

// validateExternalHostnameDNSLength validates that every resolved
// (cluster, shard) cross-product hostname is a valid RFC 1123 subdomain
// (FQDN <= 253 chars, each label <= 63 chars). The host portion is the
// substring before the last ":" (port stripped); if no ":" is present the
// entire string is the host. Iterates spec.clusters[] x
// spec.source.external.shardedCluster.shards[] (either may be empty).
func validateExternalHostnameDNSLength(s *MongoDBSearch) v1.ValidationResult {
	if !s.IsLBModeManaged() || s.Spec.LoadBalancer.Managed.ExternalHostname == "" {
		return v1.ValidationSuccess()
	}

	var clusterCount int
	if s.Spec.Clusters != nil {
		clusterCount = len(*s.Spec.Clusters)
	}

	var shardNames []string
	if s.IsExternalSourceSharded() {
		for _, sh := range s.Spec.Source.ExternalMongoDBSource.ShardedCluster.Shards {
			shardNames = append(shardNames, sh.ShardName)
		}
	}

	check := func(host string) v1.ValidationResult {
		h := host
		if idx := strings.LastIndex(h, ":"); idx >= 0 {
			h = h[:idx]
		}
		if len(h) == 0 {
			return v1.ValidationError(
				"spec.loadBalancer.managed.externalHostname resolves to an empty host: %q",
				host,
			)
		}
		// IsDNS1123Subdomain caps the FQDN at 253 chars and enforces the
		// overall regex, but does *not* enforce the per-label 63-char limit.
		// Walk the labels separately so a single oversized cluster/shard label trips here.
		if errs := validation.IsDNS1123Subdomain(h); len(errs) > 0 {
			return v1.ValidationError(
				"spec.loadBalancer.managed.externalHostname resolves to an invalid DNS subdomain %q: %s",
				h, strings.Join(errs, ", "),
			)
		}
		for _, label := range strings.Split(h, ".") {
			if errs := validation.IsDNS1123Label(label); len(errs) > 0 {
				return v1.ValidationError(
					"spec.loadBalancer.managed.externalHostname resolves to an invalid DNS subdomain %q: label %q: %s",
					h, label, strings.Join(errs, ", "),
				)
			}
		}
		return v1.ValidationSuccess()
	}

	// Iterate the cross-product. clusterCount == 0 (legacy / no spec.clusters)
	// runs a single pass with no cluster substitution; len(shardNames) == 0
	// runs a single pass with no shard substitution.
	clusterIters := clusterCount
	if clusterIters == 0 {
		clusterIters = 1
	}
	for i := 0; i < clusterIters; i++ {
		base := s.Spec.LoadBalancer.Managed.ExternalHostname
		if clusterCount > 0 {
			base = s.GetManagedLBEndpointForCluster(i)
		}
		if len(shardNames) == 0 {
			if res := check(base); res.Level == v1.ErrorLevel {
				return res
			}
			continue
		}
		for _, sn := range shardNames {
			if res := check(strings.ReplaceAll(base, ShardNamePlaceholder, sn)); res.Level == v1.ErrorLevel {
				return res
			}
		}
	}
	return v1.ValidationSuccess()
}

// validateMCRejectsUnmanagedLB rejects multi-cluster MongoDBSearch with
// spec.loadBalancer.unmanaged set. Q3-MC / Q4-MC topologies are deferred
// post-GA per spec §4.4 and §B0.2; multi-cluster at GA requires managed LB.
// Single-cluster (and the degenerate single-entry spec.clusters case) keep
// using unmanaged LB without change.
func validateMCRejectsUnmanagedLB(s *MongoDBSearch) v1.ValidationResult {
	if s.Spec.Clusters == nil || len(*s.Spec.Clusters) <= 1 {
		return v1.ValidationSuccess()
	}
	if s.Spec.LoadBalancer == nil || s.Spec.LoadBalancer.Unmanaged == nil {
		return v1.ValidationSuccess()
	}
	return v1.ValidationError(
		"Q3/Q4-MC topologies are deferred — multi-cluster MongoDBSearch requires spec.loadBalancer.managed; spec.loadBalancer.unmanaged is single-cluster only at GA",
	)
}

// validateMCRequiresLoadBalancerManaged rejects multi-cluster MongoDBSearch
// without spec.loadBalancer set at all. Q5-MC / Q6-MC ("no LB" + MC) are
// permanently rejected per spec §4.4 / §B0.2 — multi-cluster requires Envoy.
// Combined with validateMCRejectsUnmanagedLB above, this enforces the
// "MC at GA = Q1 or Q2 = managed LB" rule symbolically without inspecting
// per-cluster replicas.
func validateMCRequiresLoadBalancerManaged(s *MongoDBSearch) v1.ValidationResult {
	if s.Spec.Clusters == nil || len(*s.Spec.Clusters) <= 1 {
		return v1.ValidationSuccess()
	}
	if s.Spec.LoadBalancer != nil {
		return v1.ValidationSuccess()
	}
	return v1.ValidationError(
		"multi-cluster MongoDBSearch requires spec.loadBalancer.managed; no-LB MC topologies (Q5/Q6) are not supported",
	)
}

// validateMCMatchTagsNonEmpty rejects an explicitly-set-but-empty
// syncSourceSelector.matchTags in spec.clusters[] when len(spec.clusters) > 1.
// An empty map is meaningless: the operator cannot peek at the external
// replSetConfig to autodetect tags. Nil (omitted) is fine — inherits.
// validateClustersSyncSourceSelector covers the matchTags-vs-hosts mutual
// exclusion; this rule covers the non-nil-but-empty case.
func validateMCMatchTagsNonEmpty(s *MongoDBSearch) v1.ValidationResult {
	if s.Spec.Clusters == nil || len(*s.Spec.Clusters) <= 1 {
		return v1.ValidationSuccess()
	}
	for i, c := range *s.Spec.Clusters {
		if c.SyncSourceSelector == nil {
			continue
		}
		if c.SyncSourceSelector.MatchTags != nil && len(c.SyncSourceSelector.MatchTags) == 0 {
			return v1.ValidationError(
				"spec.clusters[%d].syncSourceSelector.matchTags cannot be empty when set; remove the field to inherit, or specify at least one tag — operator cannot autodetect tags from external mongod replSetConfig",
				i,
			)
		}
	}
	return v1.ValidationSuccess()
}
