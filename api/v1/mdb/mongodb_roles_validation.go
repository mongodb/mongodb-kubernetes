package mdb

// IMPORTANT: this package is intended to contain only "simple" validation—in
// other words, validation that is based only on the properties in the MongoDB
// resource. More complex validation, such as validation that needs to observe
// the state of the cluster, belongs somewhere else.

import (
	"net"

	"github.com/blang/semver"
	"golang.org/x/xerrors"

	v1 "github.com/mongodb/mongodb-kubernetes/api/v1"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/stringutil"
)

// Go doesn't allow us to define constant array, so we wrap it in a function

// This is the list of valid actions for pivileges defined on the DB level
func validDbActions() []string {
	return []string{
		"changeCustomData",
		"changeOwnCustomData",
		"changeOwnPassword",
		"changePassword",
		"createCollection",
		"createIndex",
		"createRole",
		"createUser",
		"dropCollection",
		"dropRole",
		"dropUser",
		"emptycapped",
		"enableProfiler",
		"grantRole",
		"killCursors",
		"listCachedAndActiveUsers",
		"revokeRole",
		"setAuthenticationRestriction",
		"unlock",
		"viewRole",
		"viewUser",
		"find",
		"insert",
		"remove",
		"update",
		"bypassDocumentValidation",
		"changeStream",
		"planCacheRead",
		"planCacheWrite",
		"planCacheIndexFilter",
		"storageDetails",
		"enableSharding",
		"getShardVersion",
		"moveChunk",
		"splitChunk",
		"splitVector",
		"collMod",
		"compact",
		"convertToCapped",
		"dropDatabase",
		"dropIndex",
		"reIndex",
		"renameCollectionSameDB",
		"repairDatabase",
		"collStats",
		"dbHash",
		"dbStats",
		"indexStats",
		"listCollections",
		"listIndexes",
		"validate",
	}
}

// This is the list of valid actions for pivileges defined on the Cluster level
func validClusterActions() []string {
	return []string{
		"bypassWriteBlockingMode",
		"checkMetadataConsistency",
		"transitionFromDedicatedConfigServer",
		"setUserWriteBlockMode",
		"setFeatureCompatibilityVersion",
		"setDefaultRWConcern",
		"rotateCertificates",
		"getClusterParameter",
		"setClusterParameter",
		"getDefaultRWConcern",
		"transitionToDedicatedConfigServer",
		"compact",
		"useUUID",
		"dropConnections",
		"killAnyCursor",
		"unlock",
		"authSchemaUpgrade",
		"cleanupOrphaned",
		"cpuProfiler",
		"inprog",
		"invalidateUserCache",
		"killop",
		"appendOplogNote",
		"replSetConfigure",
		"replSetGetConfig",
		"replSetGetStatus",
		"replSetHeartbeat",
		"replSetStateChange",
		"resync",
		"addShard",
		"flushRouterConfig",
		"getShardMap",
		"listShards",
		"removeShard",
		"shardingState",
		"applicationMessage",
		"closeAllDatabases",
		"connPoolSync",
		"forceUUID",
		"fsync",
		"getParameter",
		"hostInfo",
		"logRotate",
		"setParameter",
		"shutdown",
		"touch",
		"impersonate",
		"listSessions",
		"killAnySession",
		"connPoolStats",
		"cursorInfo",
		"diagLogging",
		"getCmdLineOpts",
		"getLog",
		"listDatabases",
		"netstat",
		"serverStatus",
		"top",
	}
}

func validateAuthenticationRestriction(ar AuthenticationRestriction) v1.ValidationResult {
	clientSources := ar.ClientSource
	serverAddresses := ar.ServerAddress

	// Validate all clientSources, they have to be either valid IP addresses or CIDR ranges
	for _, clientSource := range clientSources {
		if !isValidIp(clientSource) && !isValidCIDR(clientSource) {
			return v1.ValidationError("clientSource %s is neither a valid IP address nor a valid CIDR range", clientSource)
		}
	}

	// validate all serveraddresses, they have to be either valid IP addresses or CIDR ranges
	for _, serverAddress := range serverAddresses {
		if !isValidIp(serverAddress) && !isValidCIDR(serverAddress) {
			return v1.ValidationError("serverAddress %s is neither a valid IP address nor a valid CIDR range", serverAddress)
		}
	}

	return v1.ValidationSuccess()
}

// isVersionAtLeast takes two strings representing version (in semver notation)
// and returns true if the first one is greater or equal the second
// false otherwise
func isVersionAtLeast(mdbVersion string, expectedVersion string) (bool, error) {
	currentV, err := semver.Make(mdbVersion)
	if err != nil {
		return false, xerrors.Errorf("error parsing mdbVersion %s with semver: %w", mdbVersion, err)
	}
	expectedVersionSemver, err := semver.Make(expectedVersion)
	if err != nil {
		return false, xerrors.Errorf("error parsing mdbVersion %s with semver: %w", expectedVersion, err)
	}
	return currentV.GTE(expectedVersionSemver), nil
}

func validateClusterPrivilegeActions(actions []string, mdbVersion string) v1.ValidationResult {
	isAtLeastThreePointSix, err := isVersionAtLeast(mdbVersion, "3.6.0-0")
	if err != nil {
		return v1.ValidationError("Error when parsing version strings: %s", err)
	}
	isAtLeastFourPointTwo, err := isVersionAtLeast(mdbVersion, "4.2.0-0")
	if err != nil {
		return v1.ValidationError("Error when parsing version strings: %s", err)
	}
	invalidActionsForLessThanThreeSix := []string{"impersonate", "listSessions", "killAnySession", "useUUID", "forceUUID"}
	invalidActionsForLessThanFourTwo := []string{"dropConnections", "killAnyCursor"}

	if !isAtLeastFourPointTwo {
		// Return error if the privilege specifies actions that are not valid in MongoDB < 4.2
		if stringutil.ContainsAny(actions, invalidActionsForLessThanFourTwo...) {
			return v1.ValidationError("Some of the provided actions are not valid for MongoDB %s", mdbVersion)
		}
		if !isAtLeastThreePointSix {
			// Return error if the privilege specifies actions that are not valid in MongoDB < 3.6
			if stringutil.ContainsAny(actions, invalidActionsForLessThanThreeSix...) {
				return v1.ValidationError("Some of the provided actions are not valid for MongoDB %s", mdbVersion)
			}
		}
	}

	// Check that every action provided is valid
	for _, action := range actions {
		if !stringutil.Contains(validClusterActions(), action) {
			return v1.ValidationError("%s is not a valid cluster action", action)
		}
	}
	return v1.ValidationSuccess()
}

func validateDbPrivilegeActions(actions []string, mdbVersion string) v1.ValidationResult {
	isAtLeastThreePointSix, err := isVersionAtLeast(mdbVersion, "3.6.0-0")
	if err != nil {
		return v1.ValidationError("Error when parsing version strings: %s", err)
	}
	isAtLeastFourPointTwo, err := isVersionAtLeast(mdbVersion, "4.2.0-0")
	if err != nil {
		return v1.ValidationError("Error when parsing version strings: %s", err)
	}
	invalidActionsForLessThanThreeSix := []string{"setAuthenticationRestriction", "changeStream"}

	if !isAtLeastFourPointTwo {
		// Return error if the privilege specifies actions that are not valid in MongoDB < 4.2
		if stringutil.Contains(actions, "listCachedAndActiveUsers") {
			return v1.ValidationError("listCachedAndActiveUsers is not a valid action for MongoDB %s", mdbVersion)
		}
		if !isAtLeastThreePointSix {
			// Return error if the privilege specifies actions that are not valid in MongoDB < 3.6
			if stringutil.ContainsAny(actions, invalidActionsForLessThanThreeSix...) {
				return v1.ValidationError("Some of the provided actions are not valid for MongoDB %s", mdbVersion)
			}
		}
	}

	// Check that every action provided is valid
	for _, action := range actions {
		if !stringutil.Contains(validDbActions(), action) {
			return v1.ValidationError("%s is not a valid db action", action)
		}
	}
	return v1.ValidationSuccess()
}

func validatePrivilege(privilege Privilege, mdbVersion string) v1.ValidationResult {
	if privilege.Resource.Cluster != nil {
		if !*privilege.Resource.Cluster {
			return v1.ValidationError("The only valid value for privilege.cluster, if set, is true")
		}
		if privilege.Resource.Collection != nil || privilege.Resource.Db != nil {
			return v1.ValidationError("Cluster: true is not compatible with setting db/collection")
		}
		if res := validateClusterPrivilegeActions(privilege.Actions, mdbVersion); res.Level == v1.ErrorLevel {
			return v1.ValidationError("Actions are not valid -  %s", res.Msg)
		}
	} else {
		if res := validateDbPrivilegeActions(privilege.Actions, mdbVersion); res.Level == v1.ErrorLevel {
			return v1.ValidationError("Actions are not valid - %s", res.Msg)
		}
	}

	return v1.ValidationSuccess()
}

func isValidIp(ip string) bool {
	return net.ParseIP(ip) != nil
}

func isValidCIDR(cidr string) bool {
	_, _, err := net.ParseCIDR(cidr)
	return err == nil
}

func RoleIsCorrectlyConfigured(role MongoDBRole, mdbVersion string) v1.ValidationResult {
	// Extensive validation of the roles attribute

	if role.Role == "" {
		return v1.ValidationError("Cannot create a role with an empty name")
	}
	if role.Db == "" {
		return v1.ValidationError("Cannot create a role with an empty db")
	}

	for _, inheritedRole := range role.Roles {
		if inheritedRole.Role == "" {
			return v1.ValidationError("Cannot inherit from a role with an empty name")
		}
		if inheritedRole.Db == "" {
			return v1.ValidationError("Cannot inherit from a role with an empty db")
		}
	}
	// authenticationRestrictions:
	for _, ar := range role.AuthenticationRestrictions {
		if res := validateAuthenticationRestriction(ar); res.Level == v1.ErrorLevel {
			return v1.ValidationError("AuthenticationRestriction is invalid - %s", res.Msg)
		}
	}

	// privileges
	for _, p := range role.Privileges {
		if res := validatePrivilege(p, mdbVersion); res.Level == v1.ErrorLevel {
			return v1.ValidationError("Privilege is invalid - %s", res.Msg)
		}
	}

	return v1.ValidationSuccess()
}

func rolesAttributeIsCorrectlyConfigured(d DbCommonSpec) v1.ValidationResult {
	// Validate every single entry and return error on the first one that fails validation
	for _, role := range d.Security.Roles {
		if res := RoleIsCorrectlyConfigured(role, d.Version); res.Level == v1.ErrorLevel {
			return v1.ValidationError("Error validating role - %s", res.Msg)
		}
	}
	return v1.ValidationSuccess()
}
