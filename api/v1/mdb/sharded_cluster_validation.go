package mdb

import (
	"fmt"
	"regexp"
	"strconv"

	"k8s.io/apimachinery/pkg/util/validation"

	v1 "github.com/mongodb/mongodb-kubernetes/api/v1"
)

var MemberConfigErrorMessage = "there must be at least as many entries in MemberConfig as specified in the 'members' field"

func ShardedClusterCommonValidators() []func(m MongoDB) v1.ValidationResult {
	return []func(m MongoDB) v1.ValidationResult{
		shardOverridesShardNamesNotEmpty,
		shardOverridesShardNamesUnique,
		shardOverridesShardNamesCorrectValues,
		shardOverridesClusterSpecListsCorrect,
		shardsOrShardCountSpecified,
		shardsFieldValid,
		shardSpecificPodSpecNotUsedWithShards,
	}
}

// ShardedClusterUpdateValidators returns validators that compare the new and
// old MongoDB specs for sharded clusters. Identity of previously existing
// shards (both shardName and shardId) must be preserved across updates so that
// the operator never rewrites pre-existing Kubernetes or Ops Manager state.
func ShardedClusterUpdateValidators() []func(newObj, oldObj MongoDB) v1.ValidationResult {
	return []func(newObj, oldObj MongoDB) v1.ValidationResult{
		shardIdentityImmutable,
	}
}

func ShardedClusterSingleValidators() []func(m MongoDB) v1.ValidationResult {
	return []func(m MongoDB) v1.ValidationResult{
		emptyClusterSpecLists,
		duplicateServiceObjectsIsIgnoredInSingleCluster,
		mandatorySingleClusterFieldsAreSpecified,
	}
}

func ShardedClusterMultiValidators() []func(m MongoDB) []v1.ValidationResult {
	return []func(m MongoDB) []v1.ValidationResult{
		noIgnoredFieldUsed,
		func(m MongoDB) []v1.ValidationResult {
			return []v1.ValidationResult{hasClusterSpecListsDefined(m)}
		},
		func(m MongoDB) []v1.ValidationResult {
			return []v1.ValidationResult{validClusterSpecLists(m)}
		},
		func(m MongoDB) []v1.ValidationResult {
			return []v1.ValidationResult{validateMemberClusterIsSubsetOfKubeConfig(m)}
		},
	}
}

// shardsOrShardCountSpecified requires exactly one of spec.shardCount or
// spec.shards to be set. This preserves backwards compatibility (existing
// customers using spec.shardCount continue to work) while admitting the new
// named-shards form.
func shardsOrShardCountSpecified(m MongoDB) v1.ValidationResult {
	hasShardCount := m.Spec.ShardCount > 0
	hasShardsList := len(m.Spec.Shards) > 0

	if !hasShardCount && !hasShardsList {
		return v1.ValidationError("one of spec.shardCount or spec.shards must be specified")
	}
	if hasShardCount && hasShardsList {
		return v1.ValidationError("spec.shardCount and spec.shards are mutually exclusive; specify only one")
	}
	return v1.ValidationSuccess()
}

// shardsFieldValid enforces structural rules on spec.shards when set: every
// shardName is a DNS-1123 label, shardNames are unique, shardIds are unique,
// and each derived StatefulSet name fits within Kubernetes length limits.
// The same style of validation used by the MongoDBSearch controller.
func shardsFieldValid(m MongoDB) v1.ValidationResult {
	if len(m.Spec.Shards) == 0 {
		return v1.ValidationSuccess()
	}

	seenNames := make(map[string]struct{}, len(m.Spec.Shards))
	seenIds := make(map[string]struct{}, len(m.Spec.Shards))

	for i, shard := range m.Spec.Shards {
		if shard.ShardName == "" {
			return v1.ValidationError("spec.shards[%d].shardName is required", i)
		}
		if errs := validation.IsDNS1123Label(shard.ShardName); len(errs) > 0 {
			return v1.ValidationError("spec.shards[%d].shardName %q is not a valid DNS-1123 label: %v", i, shard.ShardName, errs)
		}
		if _, dup := seenNames[shard.ShardName]; dup {
			return v1.ValidationError("spec.shards[%d].shardName %q is a duplicate; shardNames must be unique", i, shard.ShardName)
		}
		seenNames[shard.ShardName] = struct{}{}

		shardId := shard.GetShardId()
		if _, dup := seenIds[shardId]; dup {
			return v1.ValidationError("spec.shards[%d].shardId %q is a duplicate; shardIds must be unique", i, shardId)
		}
		seenIds[shardId] = struct{}{}

		// StatefulSet names (and by extension pod hostnames) must fit within
		// a DNS-1123 label. In single-cluster the STS name equals shardName.
		// In multi-cluster topology it is fmt.Sprintf("%s-%d", shardName, clusterIdx).
		// We check the single-cluster case plus a conservative upper bound
		// for multi-cluster (clusterIdx up to 9, i.e. +2 chars).
		if errs := validation.IsDNS1123Label(shard.ShardName); len(errs) > 0 || len(shard.ShardName) > validation.DNS1123LabelMaxLength {
			return v1.ValidationError("spec.shards[%d].shardName %q exceeds the %d-character Kubernetes DNS-1123 label limit", i, shard.ShardName, validation.DNS1123LabelMaxLength)
		}
		if m.Spec.IsMultiCluster() && len(shard.ShardName)+2 > validation.DNS1123LabelMaxLength {
			return v1.ValidationError("spec.shards[%d].shardName %q is too long for multi-cluster topology; the derived StatefulSet name (shardName-<clusterIdx>) must fit within the %d-character DNS-1123 label limit", i, shard.ShardName, validation.DNS1123LabelMaxLength)
		}
	}

	return v1.ValidationSuccess()
}

// shardSpecificPodSpecNotUsedWithShards forbids the deprecated index-based
// ShardSpecificPodSpec when the new explicit shards list is used. Positional
// mapping would be ambiguous if the list is reordered.
func shardSpecificPodSpecNotUsedWithShards(m MongoDB) v1.ValidationResult {
	if len(m.Spec.Shards) > 0 && len(m.Spec.ShardSpecificPodSpec) > 0 {
		return v1.ValidationError("spec.shardSpecificPodSpec (deprecated) cannot be used together with spec.shards; use spec.shardOverrides instead")
	}
	return v1.ValidationSuccess()
}

// shardIdentityImmutable guards against updates that would silently rewrite
// Kubernetes StatefulSets or Ops Manager replica sets by mismatching shard
// identity.
//
// Two cases:
//
//  1. Transition from spec.shardCount to spec.shards (first migration). The
//     old spec had no explicit names, so the ONLY safe migration is the one
//     that preserves the synthesised identity for each pre-existing shard:
//     for i in [0, min(old.shardCount, len(new.shards))),
//     new.shards[i].shardName MUST equal "<mdb-name>-<i>".
//     Typos, reorderings, and unrelated names are rejected here.
//
//  2. Subsequent updates where both old and new use spec.shards. Identity is
//     keyed by shardName; for any shard that exists in both old and new,
//     shardId must not change. New shards may be appended (or removed),
//     and the list may be reordered freely provided existing shards keep
//     their shardId.
func shardIdentityImmutable(newObj, oldObj MongoDB) v1.ValidationResult {
	if len(newObj.Spec.Shards) == 0 {
		return v1.ValidationSuccess()
	}

	// Case 1: transition from shardCount to shards.
	if len(oldObj.Spec.Shards) == 0 && oldObj.Spec.ShardCount > 0 {
		checkLen := min(oldObj.Spec.ShardCount, len(newObj.Spec.Shards))
		for i := 0; i < checkLen; i++ {
			expected := synthesizedShardName(oldObj.Name, i)
			if newObj.Spec.Shards[i].ShardName != expected {
				return v1.ValidationError(
					"migration from spec.shardCount to spec.shards must preserve shard identity: "+
						"spec.shards[%d].shardName must be %q (matching the previously implicit shard identity), got %q. "+
						"Rename or reorder after the initial migration is complete.",
					i, expected, newObj.Spec.Shards[i].ShardName,
				)
			}
			// shardId must also be the same as the implicit identity (which
			// equals shardName when the shard was originally created from
			// shardCount).
			newId := newObj.Spec.Shards[i].GetShardId()
			if newId != expected {
				return v1.ValidationError(
					"migration from spec.shardCount to spec.shards must preserve shard identity: "+
						"spec.shards[%d].shardId must be %q (matching the previously implicit shard identity), got %q",
					i, expected, newId,
				)
			}
		}
		return v1.ValidationSuccess()
	}

	// Case 2: spec.shards → spec.shards. Match by name.
	oldByName := make(map[string]ResolvedShard, len(oldObj.Spec.Shards))
	for _, s := range oldObj.ResolvedShards() {
		oldByName[s.ShardName] = s
	}
	for _, newShard := range newObj.ResolvedShards() {
		if oldShard, ok := oldByName[newShard.ShardName]; ok {
			if oldShard.ShardId != newShard.ShardId {
				return v1.ValidationError("spec.shards[].shardId is immutable for shard %q: was %q, got %q", newShard.ShardName, oldShard.ShardId, newShard.ShardId)
			}
		}
	}

	return v1.ValidationSuccess()
}

func mandatorySingleClusterFieldsAreSpecified(m MongoDB) v1.ValidationResult {
	if m.Spec.MongodsPerShardCount == 0 ||
		m.Spec.MongosCount == 0 ||
		m.Spec.ConfigServerCount == 0 {
		return v1.ValidationError("The following fields must be specified in single cluster topology: mongodsPerShardCount, mongosCount, configServerCount")
	}
	return v1.ValidationSuccess()
}

func hasClusterSpecListsDefined(m MongoDB) v1.ValidationResult {
	msg := "cluster spec list in %s must be defined in Multi Cluster topology"
	if !hasClusterSpecList(m.Spec.ShardSpec.ClusterSpecList) {
		return v1.ValidationError(msg, "spec.shardSpec")
	}
	if !hasClusterSpecList(m.Spec.ConfigSrvSpec.ClusterSpecList) {
		return v1.ValidationError(msg, "spec.configSrvSpec")
	}
	if !hasClusterSpecList(m.Spec.MongosSpec.ClusterSpecList) {
		return v1.ValidationError(msg, "spec.mongosSpec")
	}
	return v1.ValidationSuccess()
}

func emptyClusterSpecLists(m MongoDB) v1.ValidationResult {
	msg := "cluster spec list in %s must be empty in Single Cluster topology"
	if hasClusterSpecList(m.Spec.ShardSpec.ClusterSpecList) {
		return v1.ValidationError(msg, "spec.shardSpec")
	}
	if hasClusterSpecList(m.Spec.ConfigSrvSpec.ClusterSpecList) {
		return v1.ValidationError(msg, "spec.configSrvSpec")
	}
	if hasClusterSpecList(m.Spec.MongosSpec.ClusterSpecList) {
		return v1.ValidationError(msg, "spec.mongosSpec")
	}
	for _, shardOverride := range m.Spec.ShardOverrides {
		if len(shardOverride.ClusterSpecList) > 0 {
			return v1.ValidationError(msg, "spec.shardOverrides")
		}
	}
	return v1.ValidationSuccess()
}

func hasClusterSpecList(clusterSpecList ClusterSpecList) bool {
	return len(clusterSpecList) > 0
}

// Validate clusterSpecList field, the validation for shard overrides clusterSpecList require different rules
func validClusterSpecLists(m MongoDB) v1.ValidationResult {
	clusterSpecs := []struct {
		list     ClusterSpecList
		specName string
	}{
		{m.Spec.ShardSpec.ClusterSpecList, "spec.shardSpec"},
		{m.Spec.ConfigSrvSpec.ClusterSpecList, "spec.configSrvSpec"},
		{m.Spec.MongosSpec.ClusterSpecList, "spec.mongosSpec"},
	}
	for _, spec := range clusterSpecs {
		if result := isValidClusterSpecList(spec.list, spec.specName); result != v1.ValidationSuccess() {
			return result
		}
	}
	// MemberConfig and Members fields are ignored at top level for MC Sharded
	if len(m.Spec.MemberConfig) > 0 && len(m.Spec.MemberConfig) < m.Spec.Members {
		return v1.ValidationError("Invalid clusterSpecList: %s", MemberConfigErrorMessage)
	}
	return v1.ValidationSuccess()
}

func isValidClusterSpecList(clusterSpecList ClusterSpecList, specName string) v1.ValidationResult {
	for _, clusterSpecItem := range clusterSpecList {
		if clusterSpecItem.ClusterName == "" {
			return v1.ValidationError("All clusters specified in %s.clusterSpecList require clusterName and members fields", specName)
		}
		if len(clusterSpecItem.MemberConfig) > 0 && len(clusterSpecItem.MemberConfig) < clusterSpecItem.Members {
			return v1.ValidationError("Invalid member configuration in %s.clusterSpecList: %s", specName, MemberConfigErrorMessage)
		}
	}
	return v1.ValidationSuccess()
}

func validateShardOverrideClusterSpecList(clusterSpecList []ClusterSpecItemOverride, shardNames []string) (bool, v1.ValidationResult) {
	if len(clusterSpecList) == 0 {
		return true, v1.ValidationError("shard override for shards %+v has an empty clusterSpecList", shardNames)
	}
	for _, clusterSpec := range clusterSpecList {
		// Note that it is okay for a shard override clusterSpecList to have Members = 0
		if clusterSpec.ClusterName == "" {
			return true, v1.ValidationError("shard override for shards %+v has an empty clusterName in clusterSpecList, this field must be specified", shardNames)
		}
		// This check is performed for overrides cluster spec lists as well
		if len(clusterSpec.MemberConfig) > 0 && clusterSpec.Members != nil &&
			len(clusterSpec.MemberConfig) < *clusterSpec.Members {
			return true, v1.ValidationError("shard override for shards %+v is incorrect: %s", shardNames, MemberConfigErrorMessage)
		}
	}
	return false, v1.ValidationSuccess()
}

func shardOverridesShardNamesNotEmpty(m MongoDB) v1.ValidationResult {
	for idx, shardOverride := range m.Spec.ShardOverrides {
		if len(shardOverride.ShardNames) == 0 {
			return v1.ValidationError("spec.shardOverride[*].shardNames cannot be empty, shardOverride with index %d is invalid", idx)
		}
	}
	return v1.ValidationSuccess()
}

func shardOverridesShardNamesUnique(m MongoDB) v1.ValidationResult {
	idSet := make(map[string]bool)
	for _, shardOverride := range m.Spec.ShardOverrides {
		for _, shardName := range shardOverride.ShardNames {
			if idSet[shardName] && shardName != "" {
				return v1.ValidationError("spec.shardOverride[*].shardNames elements must be unique in shardOverrides, shardName %s is a duplicate", shardName)
			}
			idSet[shardName] = true
		}
	}
	return v1.ValidationSuccess()
}

func shardOverridesShardNamesCorrectValues(m MongoDB) v1.ValidationResult {
	// When spec.shards is set, shardOverrides shardNames must refer to an
	// existing shardName in the list (set-membership validation).
	if len(m.Spec.Shards) > 0 {
		valid := make(map[string]struct{}, len(m.Spec.Shards))
		for _, s := range m.Spec.Shards {
			valid[s.ShardName] = struct{}{}
		}
		for _, shardOverride := range m.Spec.ShardOverrides {
			for _, shardName := range shardOverride.ShardNames {
				if _, ok := valid[shardName]; !ok {
					return v1.ValidationError("shardOverrides shardName %q does not refer to any shard in spec.shards", shardName)
				}
			}
		}
		return v1.ValidationSuccess()
	}

	// Legacy behaviour: when using spec.shardCount, shardOverrides shardNames
	// must match the synthesised pattern "<mdb-name>-<idx>" with idx < shardCount.
	for _, shardOverride := range m.Spec.ShardOverrides {
		for _, shardName := range shardOverride.ShardNames {
			if !validateShardName(shardName, m.Spec.ShardCount, m.Name) {
				return v1.ValidationError("name %s is incorrect, it must follow the following format: %s-{shard index} with shardIndex < %d (shardCount)", shardName, m.Name, m.Spec.ShardCount)
			}
		}
	}
	return v1.ValidationSuccess()
}

func shardOverridesClusterSpecListsCorrect(m MongoDB) v1.ValidationResult {
	for _, shardOverride := range m.Spec.ShardOverrides {
		if shardOverride.ClusterSpecList != nil {
			if hasError, result := validateShardOverrideClusterSpecList(shardOverride.ClusterSpecList, shardOverride.ShardNames); hasError {
				return result
			}
		}
	}
	return v1.ValidationSuccess()
	// Note that shardOverride.Members and shardOverride.MemberConfig should not be checked as they are ignored,
	// shardOverride.ClusterSpecList.Members and shardOverride.ClusterSpecList.MemberConfig are used instead
}

// If the MDB resource name is foo, and we have n shards, we verify that shard names ∈ {foo-0 , foo-1 ..., foo-(n-1)}
func validateShardName(shardName string, shardCount int, resourceName string) bool {
	// The shard number should not have leading zeros except for 0 itself
	pattern := fmt.Sprintf(`^%s-(0|[1-9][0-9]*)$`, resourceName)

	re := regexp.MustCompile(pattern)
	if !re.MatchString(shardName) {
		return false
	}

	// Extract the shard number from the matched part
	parts := re.FindStringSubmatch(shardName)
	shardNumber, err := strconv.Atoi(parts[1])
	if err != nil {
		return false
	}

	if shardNumber < 0 || shardNumber >= shardCount {
		return false
	}
	return true
}

func noIgnoredFieldUsed(m MongoDB) []v1.ValidationResult {
	var warnings []v1.ValidationResult
	var errors []v1.ValidationResult

	if m.Spec.MongodsPerShardCount != 0 {
		appendValidationError(&errors, "spec.mongodsPerShardCount", "spec.shard.clusterSpecList.members")
	}

	if m.Spec.MongosCount != 0 {
		appendValidationError(&errors, "spec.mongosCount", "spec.mongos.clusterSpecList.members")
	}

	if m.Spec.ConfigServerCount != 0 {
		appendValidationError(&errors, "spec.configServerCount", "spec.configSrv.clusterSpecList.members")
	}

	if m.Spec.Members != 0 {
		appendValidationWarning(&warnings, "spec.members", "spec.[...].clusterSpecList.members")
	}

	if m.Spec.MemberConfig != nil {
		appendValidationWarning(&warnings, "spec.memberConfig", "spec.[...].clusterSpecList.memberConfig")
	}

	for _, clusterSpec := range m.Spec.ShardSpec.ClusterSpecList {
		if clusterSpec.PodSpec != nil && clusterSpec.PodSpec.PodTemplateWrapper.PodTemplate != nil {
			appendValidationWarning(&warnings, "spec.shard.clusterSpecList.podSpec.podTemplate", "spec.shard.clusterSpecList.statefulSetConfiguration")
		}
	}

	for _, clusterSpec := range m.Spec.ConfigSrvSpec.ClusterSpecList {
		if clusterSpec.PodSpec != nil && clusterSpec.PodSpec.PodTemplateWrapper.PodTemplate != nil {
			appendValidationWarning(&warnings, "spec.configSrv.clusterSpecList.podSpec.podTemplate", "spec.configSrv.clusterSpecList.statefulSetConfiguration")
		}
	}

	for _, clusterSpec := range m.Spec.MongosSpec.ClusterSpecList {
		if clusterSpec.PodSpec != nil && clusterSpec.PodSpec.PodTemplateWrapper.PodTemplate != nil {
			appendValidationWarning(&warnings, "spec.mongos.clusterSpecList.podSpec.podTemplate", "spec.mongos.clusterSpecList.statefulSetConfiguration")
		}
	}

	for _, shardOverride := range m.Spec.ShardOverrides {
		if shardOverride.MemberConfig != nil {
			appendValidationWarning(&warnings, "spec.shardOverrides.memberConfig", "spec.shardOverrides.clusterSpecList.memberConfig")
		}

		if shardOverride.Members != nil {
			appendValidationWarning(&warnings, "spec.shardOverrides.members", "spec.shardOverrides.clusterSpecList.members")
		}

		if shardOverride.PodSpec != nil && shardOverride.PodSpec.PodTemplateWrapper.PodTemplate != nil {
			appendValidationWarning(&warnings, "spec.shardOverrides.podSpec.podTemplate", "spec.shardOverrides.statefulSetConfiguration")
		}

		for _, clusterSpec := range shardOverride.ClusterSpecList {
			if clusterSpec.PodSpec != nil && clusterSpec.PodSpec.PodTemplateWrapper.PodTemplate != nil {
				appendValidationWarning(&warnings, "spec.shardOverrides.clusterSpecList.podSpec.podTemplate", "spec.shardOverrides.clusterSpecList.statefulSetConfiguration")
			}
		}
	}

	if len(errors) > 0 {
		return errors
	}

	if len(warnings) > 0 {
		return warnings
	}

	return []v1.ValidationResult{v1.ValidationSuccess()}
}

func appendValidationWarning(warnings *[]v1.ValidationResult, ignoredField string, preferredField string) {
	*warnings = append(*warnings, v1.ValidationWarning("%s is ignored in Multi Cluster topology. "+
		"Use instead: %s", ignoredField, preferredField))
}

func appendValidationError(errors *[]v1.ValidationResult, ignoredField string, preferredField string) {
	*errors = append(*errors, v1.ValidationError("%s must not be set in Multi Cluster topology. "+
		"The member count will depend on: %s", ignoredField, preferredField))
}

func duplicateServiceObjectsIsIgnoredInSingleCluster(m MongoDB) v1.ValidationResult {
	if m.Spec.DuplicateServiceObjects != nil {
		return v1.ValidationWarning("In Single Cluster topology, spec.duplicateServiceObjects field is ignored")
	}
	return v1.ValidationSuccess()
}

// This is used to validate all kind of cluster spec in the same way, whether it is an override or not
func convertOverrideToClusterSpec(override ClusterSpecItemOverride) ClusterSpecItem {
	var overrideMembers int
	if override.Members != nil {
		overrideMembers = *override.Members
	} else {
		overrideMembers = 0
	}
	return ClusterSpecItem{
		ClusterName:                 override.ClusterName,
		Service:                     "",  // Field doesn't exist in override
		ExternalAccessConfiguration: nil, // Field doesn't exist in override
		Members:                     overrideMembers,
		MemberConfig:                override.MemberConfig,
		StatefulSetConfiguration:    override.StatefulSetConfiguration,
		PodSpec:                     override.PodSpec,
	}
}

func validateMemberClusterIsSubsetOfKubeConfig(m MongoDB) v1.ValidationResult {
	// We first extract every cluster spec lists from the resource (from Shard, ConfigServer, Mongos and ShardOverrides)
	// And we put them in a single flat structure, to be able to run all validations in a single for loop

	// Slice of structs to hold name and ClusterSpecList
	var clusterSpecLists []struct {
		name string
		list ClusterSpecList
	}

	// Helper function to append a ClusterSpecList to the slice
	appendClusterSpec := func(name string, list ClusterSpecList) {
		clusterSpecLists = append(clusterSpecLists, struct {
			name string
			list ClusterSpecList
		}{
			name: name,
			list: list,
		})
	}

	// Convert ClusterSpecItemOverride to ClusterSpecItem
	for _, override := range m.Spec.ShardOverrides {
		var convertedList ClusterSpecList
		for _, overrideItem := range override.ClusterSpecList {
			convertedList = append(convertedList, convertOverrideToClusterSpec(overrideItem))
		}
		appendClusterSpec(fmt.Sprintf("shard %+v override", override.ShardNames), convertedList)
	}

	// Append other ClusterSpecLists
	appendClusterSpec("spec.shardSpec", m.Spec.ShardSpec.ClusterSpecList)
	appendClusterSpec("spec.configSrvSpec", m.Spec.ConfigSrvSpec.ClusterSpecList)
	appendClusterSpec("spec.mongosSpec", m.Spec.MongosSpec.ClusterSpecList)

	// Validate each ClusterSpecList
	for _, specList := range clusterSpecLists {
		validationResult := ValidateMemberClusterIsSubsetOfKubeConfig(specList.list)
		if validationResult.Level == v1.WarningLevel {
			return v1.ValidationWarning("Warning when validating %s ClusterSpecList: %s", specList.name, validationResult.Msg)
		} else if validationResult.Level == v1.ErrorLevel {
			return v1.ValidationError("Error when validating %s ClusterSpecList: %s", specList.name, validationResult.Msg)
		}
	}

	return v1.ValidationSuccess()
}
