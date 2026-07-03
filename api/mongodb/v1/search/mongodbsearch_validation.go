package search

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"

	v1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1"
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

// isMultiCluster reports whether the spec targets more than one member cluster
// the primary topology axis the validator dispatch keys on.
func (s *MongoDBSearch) isMultiCluster() bool {
	return len(s.Spec.Clusters) > 1
}

// RunValidations dispatches into topology/source/LB-mode-keyed validator groups
// so the structure expresses the supported-configuration model. Each group's doc
// comment names the precondition the dispatch guarantees; group members rely on
// it instead of re-deriving the axis with their own guard clauses.
func (s *MongoDBSearch) RunValidations() []v1.ValidationResult {
	var results []v1.ValidationResult
	collect := func(validators []func(*MongoDBSearch) v1.ValidationResult) {
		for _, validator := range validators {
			res := validator(s)
			if res.Level > 0 {
				results = append(results, res)
			}
		}
	}

	collect(commonValidators())
	if s.IsExternalSourceSharded() {
		collect(externalShardedValidators())
	}
	if s.isMultiCluster() {
		collect(multiClusterValidators())
	}
	if s.IsLBModeManaged() {
		collect(managedLBValidators())
	} else if s.IsLBModeUnmanaged() {
		collect(unmanagedLBValidators())
	}

	return results
}

// commonValidators run for every MongoDBSearch regardless of topology, source,
// or LB mode.
func commonValidators() []func(*MongoDBSearch) v1.ValidationResult {
	return []func(*MongoDBSearch) v1.ValidationResult{
		validateClustersNonEmpty,
		validateClustersUniqueClusterName,
		validateJVMFlags,
		validateClustersEnvoyResourceNames,
		validateX509AuthConfig,
		validateLBConfig,
		validateMultipleReplicasRequireLB,
		validateShardOverrides,
	}
}

// externalShardedValidators run only when the source is an external sharded
// MongoDB cluster (IsExternalSourceSharded). The dispatch owns that guard.
func externalShardedValidators() []func(*MongoDBSearch) v1.ValidationResult {
	return []func(*MongoDBSearch) v1.ValidationResult{
		validateShardNames,
	}
}

// multiClusterValidators run only for multi-cluster specs (len(spec.clusters) >
// 1). The dispatch owns that guard, so members no longer re-check the length.
func multiClusterValidators() []func(*MongoDBSearch) v1.ValidationResult {
	return []func(*MongoDBSearch) v1.ValidationResult{
		validateClustersClusterNameNonEmpty,
		validateClustersClusterIndexRequired,
		validateMCRequiresExternalSource,
		validateMCRequiresManagedLB,
		validateMCExternalHostnames,
	}
}

// managedLBValidators run only when a managed load balancer is configured
// (IsLBModeManaged, keyed on the first cluster). validateLBConfig enforces that
// every cluster sets the same mode, so members may assume managed everywhere.
func managedLBValidators() []func(*MongoDBSearch) v1.ValidationResult {
	return []func(*MongoDBSearch) v1.ValidationResult{
		validateManagedLBExternalHostname,
		validateExternalHostnameDNSLength,
		validateRouterHostname,
	}
}

// unmanagedLBValidators run only when an unmanaged load balancer is configured
// (IsLBModeUnmanaged, keyed on the first cluster). validateLBConfig enforces that
// every cluster sets the same mode, so members may assume unmanaged everywhere.
func unmanagedLBValidators() []func(*MongoDBSearch) v1.ValidationResult {
	return []func(*MongoDBSearch) v1.ValidationResult{
		validateUnmanagedLBConfig,
		validateUnmanagedEndpointTemplate,
	}
}

// validateClustersNonEmpty is the reconcile backstop to the apiserver
// Required+MinItems=1 rule on spec.clusters. Without it an empty list would
// reconcile to a silent no-op (no units to build); surfacing it as Invalid here
// makes the misconfiguration explicit.
func validateClustersNonEmpty(s *MongoDBSearch) v1.ValidationResult {
	if len(s.Spec.Clusters) == 0 {
		return v1.ValidationError("spec.clusters must contain at least one entry")
	}
	return v1.ValidationSuccess()
}

// validateMultipleReplicasRequireLB rejects a spec that runs more than one
// mongot replica in any cluster without a load balancer to distribute traffic
// across the replicas. It depends only on spec fields, so it lives in the
// spec-validation tier.
// validateLBConfig (all-or-none) runs first, so LB presence is uniform across
// clusters and checking the first cluster covers them all.
func validateMultipleReplicasRequireLB(s *MongoDBSearch) v1.ValidationResult {
	if max := s.MaxReplicasAcrossClusters(); max > 1 && s.firstClusterLB() == nil {
		return v1.ValidationError(
			"multiple mongot replicas (%d) require load balancer configuration; "+
				"please configure load balancing in spec.clusters[].loadBalancer.",
			max,
		)
	}
	return v1.ValidationSuccess()
}

// validateLBConfig enforces the per-cluster load balancer rules:
//   - each entry sets exactly one of managed or unmanaged (the XValidation marker
//     on LoadBalancerConfig enforces this at the CRD level; this check provides
//     the same guarantee at the controller level);
//   - clusters agree on presence: all set loadBalancer, or none do;
//   - clusters agree on mode: managed and unmanaged cannot be mixed.
func validateLBConfig(s *MongoDBSearch) v1.ValidationResult {
	withLB, managedCount, unmanagedCount := 0, 0, 0
	for i, c := range s.Spec.Clusters {
		lb := c.LoadBalancer
		if lb == nil {
			continue
		}
		withLB++
		if lb.Managed == nil && lb.Unmanaged == nil {
			return v1.ValidationError("spec.clusters[%d].loadBalancer must have exactly one of 'managed' or 'unmanaged' set", i)
		}
		if lb.Managed != nil && lb.Unmanaged != nil {
			return v1.ValidationError("spec.clusters[%d].loadBalancer.managed and spec.clusters[%d].loadBalancer.unmanaged are mutually exclusive", i, i)
		}
		if lb.Managed != nil {
			managedCount++
		} else {
			unmanagedCount++
		}
	}
	if withLB > 0 && withLB != len(s.Spec.Clusters) {
		return v1.ValidationError("spec.clusters[].loadBalancer must be set on every cluster or on none; %d of %d clusters set it", withLB, len(s.Spec.Clusters))
	}
	if managedCount > 0 && unmanagedCount > 0 {
		return v1.ValidationError("spec.clusters[].loadBalancer mode must be the same on every cluster; managed and unmanaged cannot be mixed")
	}
	return v1.ValidationSuccess()
}

// validateUnmanagedLBConfig validates that every cluster's unmanaged LB specifies an endpoint.
func validateUnmanagedLBConfig(s *MongoDBSearch) v1.ValidationResult {
	for i, c := range s.Spec.Clusters {
		if c.LoadBalancer == nil || c.LoadBalancer.Unmanaged == nil {
			continue
		}
		if c.LoadBalancer.Unmanaged.Endpoint == "" {
			return v1.ValidationError("spec.clusters[%d].loadBalancer.unmanaged.endpoint must be specified when spec.clusters[%d].loadBalancer.unmanaged is configured", i, i)
		}
	}
	return v1.ValidationSuccess()
}

// validateManagedLBExternalHostname validates that every cluster sets externalHostname when
// using managed LB with an external MongoDB source (Rule 5: Envoy needs the hostname for SNI matching).
func validateManagedLBExternalHostname(s *MongoDBSearch) v1.ValidationResult {
	if !s.IsExternalMongoDBSource() {
		return v1.ValidationSuccess()
	}
	for i, c := range s.Spec.Clusters {
		if c.LoadBalancer == nil || c.LoadBalancer.Managed == nil {
			continue
		}
		if c.LoadBalancer.Managed.ExternalHostname == "" {
			return v1.ValidationError("spec.clusters[%d].loadBalancer.managed.externalHostname must be specified when using managed load balancer with an external MongoDB source", i)
		}
	}
	return v1.ValidationSuccess()
}

// validateRouterHostname validates routerHostname for an external sharded MongoDB source with managed
// LB: it is the shard-agnostic endpoint a mongos uses to reach mongot, so it is required on every
// cluster's managed LB and must NOT carry a {shardName} placeholder (it is used verbatim). Only
// applies to sharded external sources (mongos exists only in sharded topologies); ignored otherwise.
func validateRouterHostname(s *MongoDBSearch) v1.ValidationResult {
	if !s.IsExternalSourceSharded() {
		return v1.ValidationSuccess()
	}
	for i, c := range s.Spec.Clusters {
		if c.LoadBalancer == nil || c.LoadBalancer.Managed == nil {
			continue
		}
		if c.LoadBalancer.Managed.RouterHostname == "" {
			return v1.ValidationError("spec.clusters[%d].loadBalancer.managed.routerHostname must be specified when using managed load balancer with an external sharded MongoDB source", i)
		}
		if strings.Contains(c.LoadBalancer.Managed.RouterHostname, ShardNamePlaceholder) {
			return v1.ValidationError("spec.clusters[%d].loadBalancer.managed.routerHostname must not contain %s; it is the shard-agnostic endpoint for mongos", i, ShardNamePlaceholder)
		}
	}
	return v1.ValidationSuccess()
}

// isPlaceholderOnly reports whether endpoint is nothing but {shardName} placeholders
// (and whitespace), i.e. it carries no actual hostname to differentiate shards.
func isPlaceholderOnly(endpoint string) bool {
	return strings.TrimSpace(strings.ReplaceAll(endpoint, ShardNamePlaceholder, "")) == ""
}

// validateUnmanagedEndpointTemplate validates each cluster's unmanaged endpoint template against
// the source topology. For an external sharded source the endpoint must be a per-shard template:
// it must contain at least one {shardName} placeholder (Rule 6) and more than just the placeholder.
// For every other source (ReplicaSet) the endpoint must not contain a {shardName} placeholder,
// since the template makes no sense for a ReplicaSet (Rule 8).
func validateUnmanagedEndpointTemplate(s *MongoDBSearch) v1.ValidationResult {
	for i, c := range s.Spec.Clusters {
		if c.LoadBalancer == nil || c.LoadBalancer.Unmanaged == nil {
			continue
		}
		endpoint := c.LoadBalancer.Unmanaged.Endpoint
		hasTemplate := strings.Contains(endpoint, ShardNamePlaceholder)
		path := fmt.Sprintf("spec.clusters[%d].loadBalancer.unmanaged.endpoint", i)

		// External sharded: the declared shard set means each shard needs its own
		// endpoint, so the template is required and must carry more than the placeholder.
		if s.IsExternalSourceSharded() {
			if !hasTemplate {
				return v1.ValidationError("%s must contain at least one %s placeholder to differentiate between shards", path, ShardNamePlaceholder)
			}
			if isPlaceholderOnly(endpoint) {
				return v1.ValidationError("%s must contain more than just the %s placeholder", path, ShardNamePlaceholder)
			}
			continue
		}

		// External ReplicaSet: a single mongot fleet, so a {shardName} template is meaningless.
		if s.IsExternalMongoDBSource() {
			if hasTemplate {
				return v1.ValidationError("%s must not contain a %s placeholder for ReplicaSet deployments", path, ShardNamePlaceholder)
			}
			continue
		}

		// Operator-managed source: sharded-ness is only known at reconcile time, so we
		// neither require nor forbid the template, but a templated endpoint must carry
		// more than the placeholder.
		if hasTemplate && isPlaceholderOnly(endpoint) {
			return v1.ValidationError("%s must contain more than just the %s placeholder", path, ShardNamePlaceholder)
		}
	}
	return v1.ValidationSuccess()
}

// Worst-case StatefulSet pod suffix for DNS-label length validation — static
// bound so admission doesn't depend on per-cluster replica counts.
const maxPodOrdinalSuffix = "-999"

// generateShardResourceNames returns every resource name created for one (cluster, shard) pair.
// Callers should pass the largest cluster index in spec.clusters so MC deployments don't
// silently overshoot DNS limits at higher indices (clusterIndex is capped at Maximum=999).
func generateShardResourceNames(s *MongoDBSearch, shardName string, clusterIndex int) []shardResourceName {
	stsName := s.MongotStatefulSetForClusterShard(clusterIndex, shardName).Name
	resources := []shardResourceName{
		{ResourceType: "StatefulSet", Name: stsName, Standard: dnsLabel},
		{ResourceType: "Pod (max ordinal)", Name: stsName + maxPodOrdinalSuffix, Standard: dnsLabel},
		{ResourceType: "Service", Name: s.MongotServiceForClusterShard(clusterIndex, shardName).Name, Standard: dnsLabel},
		{ResourceType: "ConfigMap", Name: s.MongotConfigMapForClusterShard(clusterIndex, shardName).Name, Standard: dnsSubdomain},
	}

	if s.IsTLSConfigured() {
		resources = append(resources, shardResourceName{
			ResourceType: "TLS Certificate Secret",
			Name:         s.TLSSecretForClusterShard(clusterIndex, shardName).Name,
			Standard:     dnsSubdomain,
		})
	}

	if !s.IsShardedUnmanagedLB() {
		resources = append(resources, shardResourceName{
			ResourceType: "Proxy Service",
			Name:         s.ProxyServiceNameForClusterShard(clusterIndex, shardName).Name,
			Standard:     dnsLabel,
		})
	}

	if s.IsLBModeManaged() {
		if s.IsTLSConfigured() {
			resources = append(resources, shardResourceName{
				ResourceType: "LB Server Certificate Secret",
				Name:         s.LoadBalancerServerCertForClusterShard(clusterIndex, shardName).Name,
				Standard:     dnsSubdomain,
			})
		}
	}

	return resources
}

// validationClusterIndex returns the index admission validates clusters[position]'s
// resource-name lengths at: the pinned ClusterIndex, else the array position.
func validationClusterIndex(c ClusterSpec, position int) int {
	if c.Index != nil {
		return int(*c.Index)
	}
	return position
}

// maxValidationClusterIndex returns the largest index admission can foresee:
// the largest pinned clusterIndex, else len(spec.clusters)-1 (0 when empty).
func maxValidationClusterIndex(s *MongoDBSearch) int {
	maxIdx := len(s.Spec.Clusters) - 1
	if maxIdx < 0 {
		maxIdx = 0
	}
	for _, c := range s.Spec.Clusters {
		if c.Index != nil && int(*c.Index) > maxIdx {
			maxIdx = int(*c.Index)
		}
	}
	return maxIdx
}

func validateShardNames(s *MongoDBSearch) v1.ValidationResult {
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

		resourceNames := generateShardResourceNames(s, shardName, maxValidationClusterIndex(s))
		for _, resource := range resourceNames {
			if err := validateResourceName(resource, s.Name, shardName); err != nil {
				return v1.ValidationError("%s", err.Error())
			}
		}
	}

	return v1.ValidationSuccess()
}

// validateJVMFlags validates the JVM flags provided by the users in
// spec.clusters[].jvmFlags and spec.clusters[].shardOverrides[].jvmFlags.
// These flags are passed directly to the mongot process.
func validateJVMFlags(s *MongoDBSearch) v1.ValidationResult {
	for ci, c := range s.Spec.Clusters {
		for i, flag := range c.JVMFlags {
			if reason := invalidJVMFlagReason(flag); reason != "" {
				return v1.ValidationError("MongoDBSearch resource is invalid, spec.clusters[%d].jvmFlags[%d] %s", ci, i, reason)
			}
		}
		for oi, o := range c.ShardOverrides {
			for i, flag := range o.JVMFlags {
				if reason := invalidJVMFlagReason(flag); reason != "" {
					return v1.ValidationError("MongoDBSearch resource is invalid, spec.clusters[%d].shardOverrides[%d].jvmFlags[%d] %s", ci, oi, i, reason)
				}
			}
		}
	}

	return v1.ValidationSuccess()
}

// invalidJVMFlagReason returns why flag is not an acceptable mongot JVM flag,
// or "" when it is valid.
func invalidJVMFlagReason(flag string) string {
	switch {
	case flag == "":
		return "must not be empty"
	case strings.Contains(flag, " "):
		return fmt.Sprintf("must not contain spaces, got '%s'", flag)
	case !strings.HasPrefix(flag, "-X") && !strings.HasPrefix(flag, "-XX:") && !strings.HasPrefix(flag, "-D"):
		return fmt.Sprintf("must start with -X, -XX:, or -D, got '%s'", flag)
	case !validJVMFlagChars.MatchString(flag):
		return fmt.Sprintf("contains invalid characters, only [a-zA-Z0-9._+:-=] are allowed, got '%s'", flag)
	}
	return ""
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

// validateClustersClusterNameNonEmpty rejects an empty spec.clusters[i].name.
// The dispatch scopes this to multi-cluster specs (len > 1); the single-cluster case
// keeps allowing an empty name. Uniqueness is another validator's job; the
// dedicated "is required" message fires here so a two-empty-names spec surfaces the
// actionable hint instead of "duplicate".
func validateClustersClusterNameNonEmpty(s *MongoDBSearch) v1.ValidationResult {
	for i, c := range s.Spec.Clusters {
		if c.Name == "" {
			return v1.ValidationError(
				"spec.clusters[%d].name is required when len(spec.clusters) > 1",
				i,
			)
		}
	}
	return v1.ValidationSuccess()
}

// validateClustersClusterIndexRequired mirrors the CRD CEL requiredness + uniqueness rules
// for stale-CRD installs whose schema predates the clusterIndex markers.
func validateClustersClusterIndexRequired(s *MongoDBSearch) v1.ValidationResult {
	seen := make(map[int32]int, len(s.Spec.Clusters))
	for i, c := range s.Spec.Clusters {
		if c.Index == nil {
			return v1.ValidationError("spec.clusters[%d].index is required when len(spec.clusters) > 1", i)
		}
		if first, dup := seen[*c.Index]; dup {
			return v1.ValidationError(
				"index %d is set on more than one spec.clusters[] entry (entries %d and %d); pinned indices must be distinct",
				*c.Index, first, i,
			)
		}
		seen[*c.Index] = i
	}
	return v1.ValidationSuccess()
}

// validateClustersUniqueClusterName enforces spec.clusters[].name uniqueness.
// Empty names are skipped: validateClustersClusterNameNonEmpty owns the
// multi-cluster "is required" rule and surfaces the actionable message, so a
// two-empty-names spec must not be pre-empted here with a "duplicate" error.
func validateClustersUniqueClusterName(s *MongoDBSearch) v1.ValidationResult {
	seen := make(map[string]int, len(s.Spec.Clusters))
	for i, c := range s.Spec.Clusters {
		if c.Name == "" {
			continue
		}
		if first, dup := seen[c.Name]; dup {
			return v1.ValidationError(
				"duplicate name %q in spec.clusters (entries %d and %d)",
				c.Name, first, i,
			)
		}
		seen[c.Name] = i
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
	for i, c := range s.Spec.Clusters {
		if c.Name == "" {
			continue
		}
		idx := validationClusterIndex(c, i)
		resources := []shardResourceName{
			{
				ResourceType: "Envoy Deployment (per cluster)",
				Name:         s.LoadBalancerDeploymentNameForCluster(idx),
				Standard:     dnsLabel,
			},
			{
				ResourceType: "Envoy ConfigMap (per cluster)",
				Name:         s.LoadBalancerConfigMapNameForCluster(idx),
				Standard:     dnsSubdomain,
			},
		}
		for _, resource := range resources {
			if err := validateResourceName(resource, s.Name, c.Name); err != nil {
				return v1.ValidationError("%s", err.Error())
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

// validateMCExternalHostnames enforces, for multi-cluster specs with a managed LB and an
// external sharded source, that each cluster's externalHostname contains {shardName} so the
// per-shard SNI form is derivable. Hostnames may be shared across clusters: a cross-AZ
// failover proxy fronts multiple clusters' Envoys behind one SNI, so a given shard uses the
// same hostname in every cluster.
//
// The dispatch scopes this to multi-cluster (len > 1); the LB-mode check stays
// inline because this group is not LB-mode-scoped (it also runs for unmanaged MC,
// where there is nothing to validate).
func validateMCExternalHostnames(s *MongoDBSearch) v1.ValidationResult {
	if !s.IsLBModeManaged() {
		return v1.ValidationSuccess()
	}
	for i, c := range s.Spec.Clusters {
		if c.LoadBalancer == nil || c.LoadBalancer.Managed == nil {
			continue
		}
		tmpl := c.LoadBalancer.Managed.ExternalHostname
		if tmpl == "" {
			continue
		}
		if s.IsExternalSourceSharded() && !strings.Contains(tmpl, ShardNamePlaceholder) {
			return v1.ValidationError(
				"spec.clusters[%d].loadBalancer.managed.externalHostname must contain %s for multi-cluster sharded deployments",
				i, ShardNamePlaceholder,
			)
		}
	}
	return v1.ValidationSuccess()
}

// validateExternalHostnameDNSLength validates that every resolved
// (cluster, shard) cross-product hostname is a valid RFC 1123 subdomain
// (FQDN <= 253 chars, each label <= 63 chars). The host portion is the
// substring before the last ":" (port stripped); if no ":" is present the
// entire string is the host. Iterates spec.clusters[] (always >= 1) x
// spec.source.external.shardedCluster.shards[] (may be empty).
func validateExternalHostnameDNSLength(s *MongoDBSearch) v1.ValidationResult {
	var shardNames []string
	if s.IsExternalSourceSharded() {
		for _, sh := range s.Spec.Source.ExternalMongoDBSource.ShardedCluster.Shards {
			shardNames = append(shardNames, sh.ShardName)
		}
	}

	check := func(clusterIdx int, host string) v1.ValidationResult {
		path := fmt.Sprintf("spec.clusters[%d].loadBalancer.managed.externalHostname", clusterIdx)
		h := host
		if idx := strings.LastIndex(h, ":"); idx >= 0 {
			h = h[:idx]
		}
		if len(h) == 0 {
			return v1.ValidationError("%s resolves to an empty host: %q", path, host)
		}
		// IsDNS1123Subdomain caps the FQDN at 253 chars and enforces the
		// overall regex, but does *not* enforce the per-label 63-char limit.
		// Walk the labels separately so a single oversized cluster/shard label trips here.
		if errs := validation.IsDNS1123Subdomain(h); len(errs) > 0 {
			return v1.ValidationError(
				"%s resolves to an invalid DNS subdomain %q: %s",
				path, h, strings.Join(errs, ", "),
			)
		}
		for _, label := range strings.Split(h, ".") {
			if errs := validation.IsDNS1123Label(label); len(errs) > 0 {
				return v1.ValidationError(
					"%s resolves to an invalid DNS subdomain %q: label %q: %s",
					path, h, label, strings.Join(errs, ", "),
				)
			}
		}
		return v1.ValidationSuccess()
	}

	// Iterate the cross-product. len(shardNames) == 0 runs a single pass per
	// cluster with no shard substitution.
	for i, c := range s.Spec.Clusters {
		if c.LoadBalancer == nil || c.LoadBalancer.Managed == nil {
			continue
		}
		base := c.LoadBalancer.Managed.ExternalHostname
		if base == "" {
			continue
		}
		if len(shardNames) == 0 {
			if res := check(i, base); res.Level == v1.ErrorLevel {
				return res
			}
			continue
		}
		for _, sn := range shardNames {
			if res := check(i, strings.ReplaceAll(base, ShardNamePlaceholder, sn)); res.Level == v1.ErrorLevel {
				return res
			}
		}
	}
	return v1.ValidationSuccess()
}

// validateMCRequiresManagedLB enforces that a multi-cluster MongoDBSearch uses
// a managed load balancer on every cluster. Multi-cluster at GA requires Envoy
// (managed LB), so both a missing load balancer and an unmanaged one are rejected
// when there is more than one cluster. The dispatch scopes this to multi-cluster
// (len > 1); single-cluster keeps using no-LB / unmanaged LB.
func validateMCRequiresManagedLB(s *MongoDBSearch) v1.ValidationResult {
	for i, c := range s.Spec.Clusters {
		if c.LoadBalancer == nil {
			return v1.ValidationError(
				"multi-cluster MongoDBSearch requires a managed load balancer (spec.clusters[%d].loadBalancer.managed) at the moment; none is configured",
				i,
			)
		}
		if c.LoadBalancer.Unmanaged != nil {
			return v1.ValidationError(
				"multi-cluster MongoDBSearch requires a managed load balancer at the moment; spec.clusters[%d].loadBalancer.unmanaged is not supported for multi-cluster",
				i,
			)
		}
	}
	return v1.ValidationSuccess()
}

// validateMCRequiresExternalSource requires either external.hostAndPorts (RS source)
// or external.shardedCluster (sharded source) for multi-cluster specs:
// every cluster's mongot ConfigMap is rendered from one of those two seed shapes.
func validateMCRequiresExternalSource(s *MongoDBSearch) v1.ValidationResult {
	ext := externalSource(s)
	if ext != nil && (len(ext.HostAndPorts) > 0 || ext.ShardedCluster != nil) {
		return v1.ValidationSuccess()
	}
	return v1.ValidationError(
		"spec.source.external.hostAndPorts is required (or spec.source.external.shardedCluster " +
			"for sharded sources) when len(spec.clusters) > 1; every cluster's mongot ConfigMap " +
			"is rendered from this seed.",
	)
}

func externalSource(s *MongoDBSearch) *ExternalMongoDBSource {
	if s.Spec.Source == nil {
		return nil
	}
	return s.Spec.Source.ExternalMongoDBSource
}

// validateShardOverrides enforces the per-shard override rules:
//   - shardOverrides may only be set when the source is an external sharded cluster;
//   - every referenced shardName must exist in the declared shard set;
//   - within one cluster, a shard may appear in at most one override entry.
func validateShardOverrides(s *MongoDBSearch) v1.ValidationResult {
	hasOverrides := false
	for _, c := range s.Spec.Clusters {
		if len(c.ShardOverrides) > 0 {
			hasOverrides = true
			break
		}
	}
	if !hasOverrides {
		return v1.ValidationSuccess()
	}

	if !s.IsExternalSourceSharded() {
		return v1.ValidationError("spec.clusters[].shardOverrides is only supported for external sharded sources (spec.source.external.shardedCluster)")
	}

	declared := make(map[string]struct{})
	for _, sh := range s.Spec.Source.ExternalMongoDBSource.ShardedCluster.Shards {
		declared[sh.ShardName] = struct{}{}
	}

	for ci, c := range s.Spec.Clusters {
		seen := make(map[string]int)
		for oi, o := range c.ShardOverrides {
			for _, name := range o.ShardNames {
				if _, ok := declared[name]; !ok {
					return v1.ValidationError(
						"spec.clusters[%d].shardOverrides[%d] references unknown shardName %q; it must exist in spec.source.external.shardedCluster.shards",
						ci, oi, name,
					)
				}
				if first, dup := seen[name]; dup {
					return v1.ValidationError(
						"spec.clusters[%d]: shardName %q appears in more than one shardOverrides entry (entries %d and %d); a shard may be overridden at most once per cluster",
						ci, name, first, oi,
					)
				}
				seen[name] = oi
			}
		}
	}
	return v1.ValidationSuccess()
}
