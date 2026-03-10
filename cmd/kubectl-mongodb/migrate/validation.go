package migrate

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/spf13/cast"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/ldap"
	pkgtls "github.com/mongodb/mongodb-kubernetes/pkg/tls"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/maputil"
)

type Severity string

const (
	SeverityError   Severity = "ERROR"
	SeverityWarning Severity = "WARNING"
)

type ValidationResult struct {
	Severity Severity
	Message  string
}

var (
	defaultKeyFile                = util.AutomationAgentKeyFilePathInContainer
	defaultKeyFileWindows         = util.AutomationAgentWindowsKeyFilePath
	defaultCAFilePath             = fmt.Sprintf("%s/ca-pem", util.TLSCaMountPath)
	defaultDownloadBase           = util.PvcMmsMountPath
	defaultMonitoringAgentLogPath = fmt.Sprintf("%s/monitoring-agent.log", util.PvcMountPathLogs)
	defaultBackupAgentLogPath     = fmt.Sprintf("%s/backup-agent.log", util.PvcMountPathLogs)
	defaultAuthSchemaVersion      = om.CalculateAuthSchemaVersion()
	defaultProtocolVersion        = "1"
)

// ValidateMigration checks the automation config for fields that would conflict
// with operator defaults or are unsupported by the migration tool. Each category
// is validated independently; structural errors in one category do not block
// validation of others.
func ValidateMigration(ac *om.AutomationConfig, monitoringConfig *om.MonitoringAgentConfig, backupConfig *om.BackupAgentConfig) []ValidationResult {
	var results []ValidationResult

	processMap, err := buildProcessMap(getSlice(ac.Deployment, "processes"))
	if err != nil {
		results = append(results, ValidationResult{
			Severity: SeverityError,
			Message:  fmt.Sprintf("cannot build process map: %v", err),
		})
	}
	results = append(results, validateAuth(ac.Auth)...)
	results = append(results, validateTLS(ac.AgentSSL)...)
	results = append(results, validateAgentConfig(monitoringConfig, backupConfig)...)
	results = append(results, validateLDAP(ac.Ldap)...)
	results = append(results, validateProjectOptions(ac.Deployment)...)
	results = append(results, validateProcessConfig(ac.Deployment, processMap)...)
	results = append(results, validateReplicaSetConfig(ac.Deployment, processMap)...)
	return results
}

// validateAuth checks auth-level fields (keyFile, keyFileWindows)
// against operator-hardcoded defaults.
func validateAuth(auth *om.Auth) []ValidationResult {
	if auth == nil || auth.Disabled {
		return nil
	}

	var results []ValidationResult

	if auth.KeyFile != "" && auth.KeyFile != defaultKeyFile {
		results = append(results, ValidationResult{
			Severity: SeverityError,
			Message:  fmt.Sprintf("auth.keyFile is %q but the operator defaults to %q; this is not yet configurable through the generated CR", auth.KeyFile, defaultKeyFile),
		})
	}
	if auth.KeyFileWindows != "" && auth.KeyFileWindows != defaultKeyFileWindows {
		results = append(results, ValidationResult{
			Severity: SeverityError,
			Message:  fmt.Sprintf("auth.keyFileWindows is %q but the operator defaults to %q; this is not yet configurable through the generated CR", auth.KeyFileWindows, defaultKeyFileWindows),
		})
	}

	return results
}

// validateTLS checks project-level TLS paths (autoPEMKeyFilePath, CAFilePath)
// against operator-managed defaults.
func validateTLS(agentSSL *om.AgentSSL) []ValidationResult {
	if agentSSL == nil {
		return nil
	}

	var results []ValidationResult

	if agentSSL.AutoPEMKeyFilePath != "" {
		results = append(results, ValidationResult{
			Severity: SeverityError,
			Message:  fmt.Sprintf("tls.autoPEMKeyFilePath is %q but the operator manages this path automatically; spec.security.authentication.agents.agentCertificatePath is not yet available", agentSSL.AutoPEMKeyFilePath),
		})
	}
	if agentSSL.CAFilePath != "" && agentSSL.CAFilePath != defaultCAFilePath {
		results = append(results, ValidationResult{
			Severity: SeverityError,
			Message:  fmt.Sprintf("tls.CAFilePath is %q but the operator defaults to %q; spec.security.tls.caFilePath is not yet available", agentSSL.CAFilePath, defaultCAFilePath),
		})
	}

	return results
}

// validateAgentConfig checks monitoring and backup agent log paths against
// operator-hardcoded defaults.
func validateAgentConfig(monitoringConfig *om.MonitoringAgentConfig, backupConfig *om.BackupAgentConfig) []ValidationResult {
	var results []ValidationResult

	if monitoringConfig != nil {
		logPath := cast.ToString(monitoringConfig.BackingMap["logPath"])
		if logPath != "" && logPath != defaultMonitoringAgentLogPath {
			results = append(results, ValidationResult{
				Severity: SeverityError,
				Message:  fmt.Sprintf("monitoringAgentConfig.logPath is %q but the operator defaults to %q; spec.agent.monitoringAgent.logFilePath is not yet available", logPath, defaultMonitoringAgentLogPath),
			})
		}
	}

	if backupConfig != nil {
		logPath := cast.ToString(backupConfig.BackingMap["logPath"])
		if logPath != "" && logPath != defaultBackupAgentLogPath {
			results = append(results, ValidationResult{
				Severity: SeverityError,
				Message:  fmt.Sprintf("backupAgentConfig.logPath is %q but the operator defaults to %q; spec.agent.backupAgent.logFilePath is not yet available", logPath, defaultBackupAgentLogPath),
			})
		}
	}

	return results
}

// validateLDAP checks LDAP-specific fields that the operator handles differently
// (bindMethod is hardcoded to "simple") and warns about CA contents that require
// manual ConfigMap creation.
func validateLDAP(l *ldap.Ldap) []ValidationResult {
	if l == nil {
		return nil
	}

	var results []ValidationResult

	if l.BindMethod != "" && l.BindMethod != "simple" {
		results = append(results, ValidationResult{
			Severity: SeverityWarning,
			Message:  fmt.Sprintf("LDAP bindMethod is %q but the operator hardcodes \"simple\"; the bind method will change after migration", l.BindMethod),
		})
	}
	if l.CaFileContents != "" {
		results = append(results, ValidationResult{
			Severity: SeverityWarning,
			Message:  "LDAP CA certificate contents found in automation config; you must create a ConfigMap named \"ldap-ca\" with key \"ca.pem\" containing the CA certificate before applying the generated CR",
		})
	}

	return results
}

// validateProjectOptions checks project-level AC fields (e.g. downloadBase)
// against operator-hardcoded defaults.
func validateProjectOptions(d map[string]interface{}) []ValidationResult {
	downloadBase := maputil.ReadMapValueAsString(d, "options", "downloadBase")
	if downloadBase != "" && downloadBase != defaultDownloadBase {
		return []ValidationResult{{
			Severity: SeverityError,
			Message:  fmt.Sprintf("options.downloadBase is %q but the operator defaults to %q; spec.downloadBase is not yet available", downloadBase, defaultDownloadBase),
		}}
	}
	return nil
}

// validateProcessConfig checks all process-level fields: structure and identity,
// version compatibility, and args2_6 settings (dbPath, TLS mode/paths, sharding).
func validateProcessConfig(d map[string]interface{}, processMap map[string]map[string]interface{}) []ValidationResult {
	var results []ValidationResult

	results = append(results, checkProcessesAreValid(d)...)
	results = append(results, checkProcessesHaveVersion(d, processMap)...)
	results = append(results, checkAuthSchemaVersion(d)...)
	results = append(results, checkNonDefaultDbPath(d, processMap)...)
	results = append(results, checkTLSAllowMode(d)...)
	results = append(results, checkTLSPaths(d)...)
	results = append(results, checkShardingClusterRole(d)...)

	return results
}

// validateReplicaSetConfig checks replica set topology: single deployment per
// project, protocol version, member→process references, and member-level fields
// that are preserved or lost during migration (slaveDelay, hidden, horizons).
func validateReplicaSetConfig(d map[string]interface{}, processMap map[string]map[string]interface{}) []ValidationResult {
	var results []ValidationResult

	results = append(results, checkOneDeploymentPerProject(d)...)
	results = append(results, checkReplicaSetsExist(d)...)
	results = append(results, checkReplicaSetProtocolVersion(d)...)
	results = append(results, checkMembersReferenceProcesses(d, processMap)...)
	results = append(results, checkHeterogeneousProcessConfig(d, processMap)...)
	results = append(results, checkMemberPreservedFields(d)...)

	return results
}

func checkProcessesAreValid(d map[string]interface{}) []ValidationResult {
	processes := getSlice(d, "processes")
	if len(processes) == 0 {
		return []ValidationResult{{
			Severity: SeverityError,
			Message:  "automation config contains no processes",
		}}
	}

	var results []ValidationResult
	for i, p := range processes {
		proc, ok := p.(map[string]interface{})
		if !ok {
			results = append(results, ValidationResult{
				Severity: SeverityError,
				Message:  fmt.Sprintf("process at index %d is not a valid entry", i),
			})
			continue
		}
		name := cast.ToString(proc["name"])
		if name == "" {
			results = append(results, ValidationResult{
				Severity: SeverityError,
				Message:  fmt.Sprintf("process at index %d has no name field", i),
			})
			continue
		}

		pt := cast.ToString(proc["processType"])
		if pt != "" && pt != "mongod" {
			results = append(results, ValidationResult{
				Severity: SeverityError,
				Message:  fmt.Sprintf("process %q has processType %q but the migration tool only supports mongod replica set processes", name, pt),
			})
		}

		if disabled, ok := proc["disabled"]; ok && cast.ToBool(disabled) {
			results = append(results, ValidationResult{
				Severity: SeverityWarning,
				Message:  fmt.Sprintf("process %q is disabled in the automation config; it will be skipped during migration", name),
			})
		}
	}
	return results
}

func checkProcessesHaveVersion(d map[string]interface{}, processMap map[string]map[string]interface{}) []ValidationResult {
	replicaSets := getReplicaSets(d)
	if len(replicaSets) == 0 || processMap == nil {
		return nil
	}

	for _, m := range replicaSets[0].Members() {
		proc, ok := processMap[m.Name()]
		if !ok {
			continue
		}
		if cast.ToString(proc["version"]) != "" {
			return nil
		}
	}

	return []ValidationResult{{
		Severity: SeverityError,
		Message:  "no process in the first replica set has a version field; cannot determine MongoDB version",
	}}
}

func checkAuthSchemaVersion(d map[string]interface{}) []ValidationResult {
	var results []ValidationResult
	for _, p := range getSlice(d, "processes") {
		proc, ok := p.(map[string]interface{})
		if !ok {
			continue
		}
		name := cast.ToString(proc["name"])

		if v, ok := proc["authSchemaVersion"]; ok {
			asv := cast.ToInt(v)
			if asv != 0 && asv != defaultAuthSchemaVersion {
				results = append(results, ValidationResult{
					Severity: SeverityError,
					Message:  fmt.Sprintf("process %q has authSchemaVersion %d but the operator defaults to %d", name, asv, defaultAuthSchemaVersion),
				})
			}
		}
	}
	return results
}

func checkNonDefaultDbPath(d map[string]interface{}, processMap map[string]map[string]interface{}) []ValidationResult {
	replicaSets := getReplicaSets(d)
	if len(replicaSets) == 0 || processMap == nil {
		return nil
	}

	for _, m := range replicaSets[0].Members() {
		host := m.Name()
		proc, ok := processMap[host]
		if !ok {
			continue
		}
		dbPath := maputil.ReadMapValueAsString(proc, "args2_6", "storage", "dbPath")
		if dbPath != "" && dbPath != util.PvcMountPathData {
			return []ValidationResult{{
				Severity: SeverityWarning,
				Message:  fmt.Sprintf("process %q has storage.dbPath %q but the operator always uses %q; the dbPath will change when members transition to operator-managed", host, dbPath, util.PvcMountPathData),
			}}
		}
	}
	return nil
}

func checkTLSAllowMode(d map[string]interface{}) []ValidationResult {
	for _, p := range getSlice(d, "processes") {
		proc, ok := p.(map[string]interface{})
		if !ok {
			continue
		}
		name := cast.ToString(proc["name"])
		args := maputil.ReadMapValueAsMap(proc, "args2_6")
		if args == nil {
			continue
		}

		if !hasTLSSection(args) || pkgtls.GetTLSModeFromMongodConfig(args) == pkgtls.Disabled {
			return []ValidationResult{
				{
					Severity: SeverityWarning,
					Message:  fmt.Sprintf("process %q has no TLS configured; add spec.additionalMongodConfig.net.tls.mode: \"disabled\" to the generated CR before applying — the operator sends \"disabled\" to Ops Manager when TLS is not configured, so leaving it unset (null) is inconsistent and will cause a deployment change", name),
				},
				{
					Severity: SeverityWarning,
					Message:  "spec.security.tls is not set because the source deployment does not use TLS; do not add spec.security.tls.enabled: true unless you intend to enable TLS on all members",
				},
			}
		}
		mode := pkgtls.GetTLSModeFromMongodConfig(args)
		if mode == pkgtls.Allow || mode == "allowSSL" {
			return []ValidationResult{{
				Severity: SeverityWarning,
				Message:  fmt.Sprintf("process %q has TLS mode %q; the generated CR sets spec.security.tls.enabled with net.tls.mode in additionalMongodConfig to preserve this mode, but consider upgrading to requireTLS before migration", name, string(mode)),
			}}
		}
	}
	return nil
}

func checkTLSPaths(d map[string]interface{}) []ValidationResult {
	var results []ValidationResult
	for _, p := range getSlice(d, "processes") {
		proc, ok := p.(map[string]interface{})
		if !ok {
			continue
		}
		name := cast.ToString(proc["name"])
		net := maputil.ReadMapValueAsMap(proc, "args2_6", "net")
		if net == nil {
			continue
		}

		for _, tlsKey := range []string{"tls", "ssl"} {
			tlsSection := maputil.ReadMapValueAsMap(net, tlsKey)
			if tlsSection == nil {
				continue
			}

			certKey := cast.ToString(tlsSection["certificateKeyFile"])
			pemKey := cast.ToString(tlsSection["PEMKeyFile"])

			if certKey != "" && certKey != util.PEMKeyFilePathInContainer {
				results = append(results, ValidationResult{
					Severity: SeverityError,
					Message:  fmt.Sprintf("process %q has net.%s.certificateKeyFile %q but the operator defaults to %q; the certificate path will change after migration", name, tlsKey, certKey, util.PEMKeyFilePathInContainer),
				})
			}
			if pemKey != "" && pemKey != util.PEMKeyFilePathInContainer {
				results = append(results, ValidationResult{
					Severity: SeverityError,
					Message:  fmt.Sprintf("process %q has net.%s.PEMKeyFile %q but the operator defaults to %q; the certificate path will change after migration", name, tlsKey, pemKey, util.PEMKeyFilePathInContainer),
				})
			}

			expectedClusterFile := fmt.Sprintf("%s%s-pem", util.InternalClusterAuthMountPath, name)
			if clusterFile := cast.ToString(tlsSection["clusterFile"]); clusterFile != "" && clusterFile != expectedClusterFile {
				results = append(results, ValidationResult{
					Severity: SeverityError,
					Message:  fmt.Sprintf("process %q has net.%s.clusterFile %q but the operator defaults to %q; the cluster file path will change after migration", name, tlsKey, clusterFile, expectedClusterFile),
				})
			}
		}
	}
	return results
}

func checkShardingClusterRole(d map[string]interface{}) []ValidationResult {
	var results []ValidationResult
	for _, p := range getSlice(d, "processes") {
		proc, ok := p.(map[string]interface{})
		if !ok {
			continue
		}
		name := cast.ToString(proc["name"])
		role := maputil.ReadMapValueAsString(proc, "args2_6", "sharding", "clusterRole")
		if role != "" {
			results = append(results, ValidationResult{
				Severity: SeverityError,
				Message:  fmt.Sprintf("process %q has sharding.clusterRole %q; the migration tool only supports standalone replica sets, not config server or shard replica sets", name, role),
			})
		}
	}
	return results
}

func checkOneDeploymentPerProject(d map[string]interface{}) []ValidationResult {
	count := countDeployments(d)
	if count <= 1 {
		return nil
	}
	return []ValidationResult{{
		Severity: SeverityError,
		Message:  fmt.Sprintf("project contains %d deployments but the operator requires exactly one deployment per Ops Manager project; split the project before migrating", count),
	}}
}

func countDeployments(d map[string]interface{}) int {
	sharding := getSlice(d, "sharding")
	shardedCount := len(sharding)

	shardRSNames := map[string]bool{}
	for _, s := range sharding {
		sMap, ok := s.(map[string]interface{})
		if !ok {
			continue
		}
		for _, sh := range getSlice(sMap, "shards") {
			if shMap, ok := sh.(map[string]interface{}); ok {
				shardRSNames[cast.ToString(shMap["_id"])] = true
			}
		}
	}

	independentRSCount := 0
	for _, rs := range getReplicaSets(d) {
		if !shardRSNames[rs.Name()] {
			independentRSCount++
		}
	}

	return shardedCount + independentRSCount
}

func checkReplicaSetsExist(d map[string]interface{}) []ValidationResult {
	if len(getReplicaSets(d)) == 0 {
		return []ValidationResult{{
			Severity: SeverityError,
			Message:  "automation config contains no replicaSets; only replica set deployments can be migrated",
		}}
	}
	return nil
}

func checkReplicaSetProtocolVersion(d map[string]interface{}) []ValidationResult {
	var results []ValidationResult
	for _, rs := range getReplicaSets(d) {
		rsID := rs.Name()
		pv := cast.ToString(rs["protocolVersion"])
		if pv != "" && pv != defaultProtocolVersion {
			results = append(results, ValidationResult{
				Severity: SeverityError,
				Message:  fmt.Sprintf("replica set %q has protocolVersion %q but the operator only supports %q", rsID, pv, defaultProtocolVersion),
			})
		}
	}
	return results
}

func checkMembersReferenceProcesses(d map[string]interface{}, processMap map[string]map[string]interface{}) []ValidationResult {
	replicaSets := getReplicaSets(d)
	if len(replicaSets) == 0 || processMap == nil {
		return nil
	}

	var results []ValidationResult
	for _, rs := range replicaSets {
		rsID := rs.Name()
		members := rs.Members()

		if len(members) == 0 {
			results = append(results, ValidationResult{
				Severity: SeverityError,
				Message:  fmt.Sprintf("replica set %q has no members", rsID),
			})
			continue
		}

		for _, m := range members {
			host := m.Name()
			if _, ok := processMap[host]; !ok {
				results = append(results, ValidationResult{
					Severity: SeverityError,
					Message:  fmt.Sprintf("member %q in replica set %q references a process not found in the automation config", host, rsID),
				})
			}
		}
	}
	return results
}

// checkHeterogeneousProcessConfig warns when replica set members have different
// additionalMongodConfig-relevant settings. Only fields that end up in
// spec.additionalMongodConfig are compared, since those must be uniform
// across all members in Kubernetes. Operator-managed or per-process fields
// (systemLog, TLS paths, security.clusterAuthMode, etc.) are excluded.
//
// Fields that differ between members are excluded from the generated CR
// because Kubernetes applies additionalMongodConfig uniformly to every pod.
// A warning is emitted for each excluded field so the user can review and
// reconcile the processes before migration.
func checkHeterogeneousProcessConfig(d map[string]interface{}, processMap map[string]map[string]interface{}) []ValidationResult {
	replicaSets := getReplicaSets(d)
	if len(replicaSets) == 0 || processMap == nil {
		return nil
	}

	members := replicaSets[0].Members()
	if len(members) < 2 {
		return nil
	}

	var allFlat []map[string]string
	for _, m := range members {
		host := m.Name()
		proc, ok := processMap[host]
		if !ok {
			continue
		}
		args := maputil.ReadMapValueAsMap(proc, "args2_6")
		if args == nil {
			continue
		}
		cfg := mdbv1.NewEmptyAdditionalMongodConfig()
		extractNetConfig(args, cfg)
		extractStorageConfig(args, cfg)
		extractReplicationConfig(args, cfg)
		extractGenericSections(args, cfg)
		extractNonDefaultTLSMode(args, cfg)
		allFlat = append(allFlat, flattenConfigToKeyValues(cfg.ToMap(), ""))
	}

	if len(allFlat) < 2 {
		return nil
	}

	allKeys := collectAllKeys(allFlat)

	var results []ValidationResult
	for _, key := range allKeys {
		if !isConsistentAcrossMembers(allFlat, key) {
			results = append(results, ValidationResult{
				Severity: SeverityWarning,
				Message:  fmt.Sprintf("additionalMongodConfig field %q differs between processes in the replica set; this field will be excluded from the generated CR because Kubernetes applies it uniformly to all members — reconcile the processes before migration or set it manually after", key),
			})
		}
	}
	return results
}

// flattenConfigToKeyValues recursively walks a nested map and produces a flat
// map of dotted keys to JSON-serialized leaf values (e.g. "storage.engine" → "\"inMemory\"").
func flattenConfigToKeyValues(m map[string]interface{}, prefix string) map[string]string {
	result := make(map[string]string)
	for k, v := range m {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		if sub, ok := v.(map[string]interface{}); ok {
			for sk, sv := range flattenConfigToKeyValues(sub, key) {
				result[sk] = sv
			}
		} else {
			b, _ := json.Marshal(v)
			result[key] = string(b)
		}
	}
	return result
}

// collectAllKeys returns a sorted deduplicated list of all keys across all
// flat config maps.
func collectAllKeys(allFlat []map[string]string) []string {
	seen := map[string]bool{}
	for _, flat := range allFlat {
		for k := range flat {
			seen[k] = true
		}
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// isConsistentAcrossMembers returns true if every member has the same
// serialized value for the given key (or the key is absent in all).
func isConsistentAcrossMembers(allFlat []map[string]string, key string) bool {
	var refVal string
	refSet := false
	for _, flat := range allFlat {
		val, exists := flat[key]
		if !refSet {
			refVal = val
			refSet = exists
			continue
		}
		if exists != refSet || val != refVal {
			return false
		}
	}
	return true
}

func checkMemberPreservedFields(d map[string]interface{}) []ValidationResult {
	var results []ValidationResult
	for _, rs := range getReplicaSets(d) {
		rsID := rs.Name()

		for _, m := range rs.Members() {
			host := m.Name()

			if delay := cast.ToInt(m["slaveDelay"]); delay > 0 {
				results = append(results, ValidationResult{
					Severity: SeverityWarning,
					Message:  fmt.Sprintf("member %q in replica set %q has slaveDelay=%d; this value is preserved while the member remains external but will be lost when the member transitions to operator-managed", host, rsID, delay),
				})
			}

			if hidden, ok := m["hidden"]; ok && cast.ToBool(hidden) {
				results = append(results, ValidationResult{
					Severity: SeverityWarning,
					Message:  fmt.Sprintf("member %q in replica set %q is hidden; this value is preserved while the member remains external but will be lost when the member transitions to operator-managed", host, rsID),
				})
			}

			if _, ok := m["horizons"]; ok {
				results = append(results, ValidationResult{
					Severity: SeverityWarning,
					Message:  fmt.Sprintf("member %q in replica set %q has horizons configured; horizons are VM-specific and will be overwritten by the operator for Kubernetes-managed members", host, rsID),
				})
			}
		}
	}
	return results
}
