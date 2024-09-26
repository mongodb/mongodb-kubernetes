package mdb

import (
	"fmt"
	"regexp"
	"strconv"

	v1 "github.com/10gen/ops-manager-kubernetes/api/v1"
)

var MemberConfigErrorMessage = "there must be at least as many entries in MemberConfig as specified in the 'members' field"

func ShardedClusterCommonValidators() []func(m MongoDB) v1.ValidationResult {
	return []func(m MongoDB) v1.ValidationResult{
		shardOverridesShardNamesNotEmpty,
		shardOverridesShardNamesUnique,
		shardOverridesShardNamesCorrectValues,
		shardOverridesClusterSpecListsCorrect,
	}
}

func ShardedClusterSingleValidators() []func(m MongoDB) v1.ValidationResult {
	return []func(m MongoDB) v1.ValidationResult{
		emptyClusterSpecLists,
		duplicateServiceObjectsIsIgnoredInSingleCluster,
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

func hasClusterSpecListsDefined(m MongoDB) v1.ValidationResult {
	msg := "cluster spec list in %s must be defined in Multi Cluster topology"
	if !hasClusterSpecList(m.Spec.ShardSpec.ClusterSpecList) {
		return v1.ValidationError(fmt.Sprintf(msg, "spec.shardSpec"))
	}
	if !hasClusterSpecList(m.Spec.ConfigSrvSpec.ClusterSpecList) {
		return v1.ValidationError(fmt.Sprintf(msg, "spec.configSrvSpec"))
	}
	if !hasClusterSpecList(m.Spec.MongosSpec.ClusterSpecList) {
		return v1.ValidationError(fmt.Sprintf(msg, "spec.mongosSpec"))
	}
	return v1.ValidationSuccess()
}

func emptyClusterSpecLists(m MongoDB) v1.ValidationResult {
	msg := "cluster spec list in %s must be empty in Single Cluster topology"
	if hasClusterSpecList(m.Spec.ShardSpec.ClusterSpecList) {
		return v1.ValidationError(fmt.Sprintf(msg, "spec.shardSpec"))
	}
	if hasClusterSpecList(m.Spec.ConfigSrvSpec.ClusterSpecList) {
		return v1.ValidationError(fmt.Sprintf(msg, "spec.configSrvSpec"))
	}
	if hasClusterSpecList(m.Spec.MongosSpec.ClusterSpecList) {
		return v1.ValidationError(fmt.Sprintf(msg, "spec.mongosSpec"))
	}
	for _, shardOverride := range m.Spec.ShardOverrides {
		if shardOverride.ClusterSpecList != nil && len(shardOverride.ClusterSpecList) > 0 {
			return v1.ValidationError(fmt.Sprintf(msg, "spec.shardOverrides"))
		}
	}
	return v1.ValidationSuccess()
}

func hasClusterSpecList(clusterSpecList ClusterSpecList) bool {
	return len(clusterSpecList) > 0
}

// Validate clusterSpecList field, the validation for shard overrides clusterSpecList require different rules
func validClusterSpecLists(m MongoDB) v1.ValidationResult {
	msg := "All clusters specified in %s.clusterSpecList require clusterName and members fields"
	if !isValidClusterSpecList(m.Spec.ShardSpec.ClusterSpecList) {
		return v1.ValidationError(fmt.Sprintf(msg, "spec.shardSpec"))
	}
	if !isValidClusterSpecList(m.Spec.ConfigSrvSpec.ClusterSpecList) {
		return v1.ValidationError(fmt.Sprintf(msg, "spec.configSrvSpec"))
	}
	if !isValidClusterSpecList(m.Spec.MongosSpec.ClusterSpecList) {
		return v1.ValidationError(fmt.Sprintf(msg, "spec.congosSpec"))
	}
	if len(m.Spec.MemberConfig) > 0 && len(m.Spec.MemberConfig) < m.Spec.Members {
		configErrorMessage := "Invalid clusterSpecList: " + MemberConfigErrorMessage
		return v1.ValidationError(configErrorMessage)
	}
	return v1.ValidationSuccess()
}

func isValidClusterSpecList(clusterSpecList ClusterSpecList) bool {
	for _, clusterSpecItem := range clusterSpecList {
		if clusterSpecItem.ClusterName == "" || clusterSpecItem.Members == 0 {
			return false
		}
	}
	return true
}

func validateShardOverrideClusterSpecList(clusterSpecList []ClusterSpecItemOverride, shardNames []string) (bool, v1.ValidationResult) {
	if len(clusterSpecList) == 0 {
		msg := fmt.Sprintf("shard override for shards %+v has an empty clusterSpecList", shardNames)
		return true, v1.ValidationError(msg)
	}
	for _, clusterSpec := range clusterSpecList {
		// Note that it is okay for a shard override clusterSpecList to have Members = 0
		if clusterSpec.ClusterName == "" {
			msg := fmt.Sprintf("shard override for shards %+v has an empty clusterName in clusterSpecList, this field must be specified", shardNames)
			return true, v1.ValidationError(msg)
		}
		// This check is performed for overrides cluster spec lists as well
		if len(clusterSpec.MemberConfig) > 0 && clusterSpec.Members != nil &&
			len(clusterSpec.MemberConfig) < *clusterSpec.Members {
			memberConfigErrorMessage := fmt.Sprintf("shard override for shards %+v is incorrect: %s", shardNames, MemberConfigErrorMessage)
			return true, v1.ValidationError(memberConfigErrorMessage)
		}
	}
	return false, v1.ValidationSuccess()
}

func shardOverridesShardNamesNotEmpty(m MongoDB) v1.ValidationResult {
	for idx, shardOverride := range m.Spec.ShardOverrides {
		if shardOverride.ShardNames == nil || len(shardOverride.ShardNames) == 0 {
			msg := fmt.Sprintf("spec.shardOverride[*].shardNames cannot be empty, shardOverride with index %d is invalid", idx)
			return v1.ValidationError(msg)
		}
	}
	return v1.ValidationSuccess()
}

func shardOverridesShardNamesUnique(m MongoDB) v1.ValidationResult {
	idSet := make(map[string]bool)
	for _, shardOverride := range m.Spec.ShardOverrides {
		for _, shardName := range shardOverride.ShardNames {
			if idSet[shardName] && shardName != "" {
				msg := fmt.Sprintf("spec.shardOverride[*].shardNames elements must be unique in shardOverrides, shardName %s is a duplicate", shardName)
				return v1.ValidationError(msg)
			}
			idSet[shardName] = true
		}
	}
	return v1.ValidationSuccess()
}

func shardOverridesShardNamesCorrectValues(m MongoDB) v1.ValidationResult {
	for _, shardOverride := range m.Spec.ShardOverrides {
		for _, shardName := range shardOverride.ShardNames {
			if !validateShardName(shardName, m.Spec.ShardCount, m.Name) {
				msg := fmt.Sprintf("name %s is incorrect, it must follow the following format: %s-{shard index} with shardIndex < %d (shardCount)", shardName, m.Name, m.Spec.ShardCount)
				return v1.ValidationError(msg)
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

	for _, shardOverride := range m.Spec.ShardOverrides {
		if shardOverride.MemberConfig != nil {
			appendValidationWarning(&warnings, "spec.shardOverrides.memberConfig", "spec.shardOverrides.clusterSpecList.memberConfig")
		}

		if shardOverride.Members != nil {
			appendValidationWarning(&warnings, "spec.shardOverrides.members", "spec.shardOverrides.clusterSpecList.members")
		}

		if shardOverride.StatefulSetConfiguration != nil {
			appendValidationWarning(&warnings, "spec.shardOverrides.statefulSetConfiguration", "spec.shardOverrides.clusterSpecList.statefulSetConfiguration")
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
		Service:                     "", // Field doesn't exist in override
		ExternalAccessConfiguration: override.ExternalAccessConfiguration,
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
		if validationResult.Level > 0 {
			return v1.ValidationError(fmt.Sprintf("Error when validating %s ClusterSpecList: %s", specList.name, validationResult.Msg))
		}
	}

	return v1.ValidationSuccess()
}
