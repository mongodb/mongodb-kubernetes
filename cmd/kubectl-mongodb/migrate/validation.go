package migrate

import (
	"fmt"
	"maps"
	"slices"

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
func ValidateMigration(ac *om.AutomationConfig, projectAgentConfigs *ProjectAgentConfigs, projectProcessConfigs *ProjectProcessConfigs) []ValidationResult {
	var results []ValidationResult

	processMap := ac.Deployment.ProcessMap()
	results = append(results, validateAuth(ac.Auth)...)
	results = append(results, validateTLS(ac.AgentSSL)...)
	results = append(results, validateAgentConfig(projectAgentConfigs)...)
	results = append(results, validateLDAP(ac.Ldap)...)
	results = append(results, validateProjectOptions(ac.Deployment)...)
	results = append(results, validateProcessConfig(ac.Deployment, processMap)...)
	results = append(results, validateReplicaSetConfig(ac.Deployment, processMap, projectProcessConfigs)...)
	return results
}

// validateAuth checks auth-level fields (keyFile, keyFileWindows, autoUser)
// against operator-hardcoded defaults.
func validateAuth(auth *om.Auth) []ValidationResult {
	if auth == nil || auth.Disabled {
		return nil
	}

	var results []ValidationResult

	if auth.AutoUser == "" {
		results = append(results, ValidationResult{
			Severity: SeverityError,
			Message:  "auth.autoUser is empty; the operator requires an automation agent user when auth is enabled",
		})
	} else if auth.AutoAuthMechanism != "MONGODB-X509" {
		// X509 agents authenticate with their certificate subject DN,
		// not a database user in usersWanted.
		hasMatchingUser := slices.ContainsFunc(auth.Users, func(u *om.MongoDBUser) bool {
			return u != nil && u.Username == auth.AutoUser && u.Database == util.DefaultUserDatabase
		})
		if !hasMatchingUser {
			results = append(results, ValidationResult{
				Severity: SeverityError,
				Message:  fmt.Sprintf("auth.autoUser=%q has no matching entry in auth.usersWanted (db: %q); agent auth will fail after migration", auth.AutoUser, util.DefaultUserDatabase),
			})
		}
	}

	if auth.KeyFile != "" && auth.KeyFile != defaultKeyFile {
		results = append(results, ValidationResult{
			Severity: SeverityError,
			Message:  fmt.Sprintf("auth.keyFile=%q differs from operator default %q; not configurable via CR", auth.KeyFile, defaultKeyFile),
		})
	}
	if auth.KeyFileWindows != "" && auth.KeyFileWindows != defaultKeyFileWindows {
		results = append(results, ValidationResult{
			Severity: SeverityError,
			Message:  fmt.Sprintf("auth.keyFileWindows=%q differs from operator default %q; not configurable via CR", auth.KeyFileWindows, defaultKeyFileWindows),
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
			Severity: SeverityWarning,
			Message:  fmt.Sprintf("tls.autoPEMKeyFilePath=%q will be overwritten by the operator during reconciliation", agentSSL.AutoPEMKeyFilePath),
		})
	}
	if agentSSL.CAFilePath != "" && agentSSL.CAFilePath != defaultCAFilePath {
		results = append(results, ValidationResult{
			Severity: SeverityWarning,
			Message:  fmt.Sprintf("tls.CAFilePath=%q differs from operator default %q and will be overwritten", agentSSL.CAFilePath, defaultCAFilePath),
		})
	}

	return results
}

// validateAgentConfig checks monitoring and backup agent log paths against
// operator-hardcoded defaults.
func validateAgentConfig(configs *ProjectAgentConfigs) []ValidationResult {
	if configs == nil {
		return nil
	}

	var results []ValidationResult

	if configs.MonitoringConfig != nil {
		logPath := configs.MonitoringConfig.LogPath()
		if logPath != "" && logPath != defaultMonitoringAgentLogPath {
			results = append(results, ValidationResult{
				Severity: SeverityError,
				Message:  fmt.Sprintf("monitoringAgentConfig.logPath=%q differs from operator default %q; not configurable via CR", logPath, defaultMonitoringAgentLogPath),
			})
		}
	}

	if configs.BackupConfig != nil {
		logPath := configs.BackupConfig.LogPath()
		if logPath != "" && logPath != defaultBackupAgentLogPath {
			results = append(results, ValidationResult{
				Severity: SeverityError,
				Message:  fmt.Sprintf("backupAgentConfig.logPath=%q differs from operator default %q; not configurable via CR", logPath, defaultBackupAgentLogPath),
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
			Message:  fmt.Sprintf("LDAP bindMethod=%q will change to \"simple\" (operator default) after migration", l.BindMethod),
		})
	}
	if l.CaFileContents != "" {
		results = append(results, ValidationResult{
			Severity: SeverityWarning,
			Message:  "LDAP CA certificate found; create ConfigMap \"ldap-ca\" with key \"ca.pem\" before applying the CR",
		})
	}

	return results
}

// validateProjectOptions checks project-level AC fields (e.g. downloadBase)
// against operator-hardcoded defaults.
func validateProjectOptions(d om.Deployment) []ValidationResult {
	downloadBase := d.DownloadBase()
	if downloadBase != defaultDownloadBase {
		return []ValidationResult{{
			Severity: SeverityError,
			Message:  fmt.Sprintf("options.downloadBase=%q differs from operator default %q; not configurable via CR", downloadBase, defaultDownloadBase),
		}}
	}
	return nil
}

// validateProcessConfig checks all process-level fields: structure and identity,
// version compatibility, and args2_6 settings (dbPath, TLS mode/paths, sharding).
func validateProcessConfig(d om.Deployment, processMap map[string]om.Process) []ValidationResult {
	var results []ValidationResult

	results = append(results, checkProcessesAreValid(d)...)
	results = append(results, checkAuthSchemaVersion(d)...)
	results = append(results, checkNonDefaultDbPath(d, processMap)...)
	results = append(results, checkTLSAllowMode(d)...)
	results = append(results, checkTLSPaths(d)...)
	results = append(results, checkShardingClusterRole(d)...)

	return results
}

// validateReplicaSetConfig checks replica set topology: single deployment per
// project, protocol version, member→process references, and member-level fields
// that are preserved or lost during migration (slaveDelay, hidden).
func validateReplicaSetConfig(d om.Deployment, processMap map[string]om.Process, projectProcessConfigs *ProjectProcessConfigs) []ValidationResult {
	var results []ValidationResult

	results = append(results, checkOneDeploymentPerProject(d)...)
	results = append(results, checkReplicaSetsExist(d)...)
	results = append(results, checkReplicaSetProtocolVersion(d)...)
	results = append(results, checkMembersReferenceProcesses(d, processMap)...)
	results = append(results, checkHeterogeneousProcessConfig(d, processMap)...)
	results = append(results, checkProcessConfigDrift(d, processMap, projectProcessConfigs)...)
	results = append(results, checkVersionConsistency(d, processMap)...)
	results = append(results, checkMemberPreservedFields(d)...)

	return results
}

func checkProcessesAreValid(d om.Deployment) []ValidationResult {
	processes := d.GetProcesses()
	if len(processes) == 0 {
		return []ValidationResult{{
			Severity: SeverityError,
			Message:  "automation config contains no processes",
		}}
	}

	var results []ValidationResult
	for _, proc := range processes {
		name := proc.Name()

		if proc.ProcessType() != om.ProcessTypeMongod {
			results = append(results, ValidationResult{
				Severity: SeverityError,
				Message:  fmt.Sprintf("process %q has processType=%q; only mongod replica set processes are supported", name, string(proc.ProcessType())),
			})
		}

		if proc.IsDisabled() {
			results = append(results, ValidationResult{
				Severity: SeverityWarning,
				Message:  fmt.Sprintf("process %q is disabled and will be skipped", name),
			})
		}
	}
	return results
}

func checkAuthSchemaVersion(d om.Deployment) []ValidationResult {
	var results []ValidationResult
	for _, proc := range d.GetProcesses() {
		asv := proc.AuthSchemaVersion()
		if asv != defaultAuthSchemaVersion {
			results = append(results, ValidationResult{
				Severity: SeverityError,
				Message:  fmt.Sprintf("process %q has authSchemaVersion=%d; operator default is %d", proc.Name(), asv, defaultAuthSchemaVersion),
			})
		}
	}
	return results
}

func checkNonDefaultDbPath(d om.Deployment, processMap map[string]om.Process) []ValidationResult {
	replicaSets := d.GetReplicaSets()
	if len(replicaSets) == 0 {
		return nil
	}

	for _, m := range replicaSets[0].Members() {
		host := m.Name()
		proc, ok := processMap[host]
		if !ok {
			continue
		}
		dbPath := proc.DbPath()
		if dbPath != "" && dbPath != util.PvcMountPathData {
			return []ValidationResult{{
				Severity: SeverityWarning,
				Message:  fmt.Sprintf("process %q has dbPath=%q; operator uses %q — path changes when member becomes operator-managed", host, dbPath, util.PvcMountPathData),
			}}
		}
	}
	return nil
}

func checkTLSAllowMode(d om.Deployment) []ValidationResult {
	var results []ValidationResult
	noTLSReported := false

	for _, proc := range d.GetProcesses() {
		args := proc.Args()

		if !hasTLSSection(args) || pkgtls.GetTLSModeFromMongodConfig(args) == pkgtls.Disabled {
			if !noTLSReported {
				results = append(results, ValidationResult{
					Severity: SeverityWarning,
					Message:  fmt.Sprintf("process %q has no TLS; add net.tls.mode: \"disabled\" in spec.additionalMongodConfig to avoid deployment drift", proc.Name()),
				}, ValidationResult{
					Severity: SeverityWarning,
					Message:  "spec.security.tls is not set; do not add tls.enabled: true unless you intend to enable TLS on all members",
				})
				noTLSReported = true
			}
			continue
		}
		mode := pkgtls.GetTLSModeFromMongodConfig(args)
		if mode == pkgtls.Allow || mode == "allowSSL" {
			results = append(results, ValidationResult{
				Severity: SeverityWarning,
				Message:  fmt.Sprintf("process %q uses TLS mode %q; consider upgrading to \"requireTLS\" before migration", proc.Name(), string(mode)),
			})
		}
	}
	return results
}

func checkTLSPaths(d om.Deployment) []ValidationResult {
	var results []ValidationResult
	for _, proc := range d.GetProcesses() {
		name := proc.Name()
		sections := proc.NetTLSSections()

		for tlsKey, tlsSection := range sections {
			certKey, _ := tlsSection["certificateKeyFile"].(string)
			pemKey, _ := tlsSection["PEMKeyFile"].(string)

			if certKey != "" && certKey != util.PEMKeyFilePathInContainer {
				results = append(results, ValidationResult{
					Severity: SeverityError,
					Message:  fmt.Sprintf("process %q has net.%s.certificateKeyFile %q but the operator defaults to %q; the certificate path will change after migration", name, tlsKey, certKey, util.PEMKeyFilePathInContainer),
				})
			}
			if pemKey != "" && pemKey != util.PEMKeyFilePathInContainer {
				results = append(results, ValidationResult{
					Severity: SeverityError,
					Message:  fmt.Sprintf("process %q has net.%s.PEMKeyFile=%q; operator default is %q", name, tlsKey, pemKey, util.PEMKeyFilePathInContainer),
				})
			}

			expectedClusterFile := fmt.Sprintf("%s%s-pem", util.InternalClusterAuthMountPath, name)
			if clusterFile, _ := tlsSection["clusterFile"].(string); clusterFile != "" && clusterFile != expectedClusterFile {
				results = append(results, ValidationResult{
					Severity: SeverityError,
					Message:  fmt.Sprintf("process %q has net.%s.clusterFile=%q; operator default is %q", name, tlsKey, clusterFile, expectedClusterFile),
				})
			}
		}
	}
	return results
}

func checkShardingClusterRole(d om.Deployment) []ValidationResult {
	var results []ValidationResult
	for _, proc := range d.GetProcesses() {
		role := proc.ShardingClusterRole()
		if role != "" {
			results = append(results, ValidationResult{
				Severity: SeverityError,
				Message:  fmt.Sprintf("process %q has sharding.clusterRole=%q; only standalone replica sets are supported", proc.Name(), role),
			})
		}
	}
	return results
}

func checkOneDeploymentPerProject(d om.Deployment) []ValidationResult {
	count := countDeployments(d)
	if count <= 1 {
		return nil
	}
	return []ValidationResult{{
		Severity: SeverityError,
		Message:  fmt.Sprintf("project contains %d deployments; operator requires exactly one per project — split before migrating", count),
	}}
}

func countDeployments(d om.Deployment) int {
	shardedClusters := d.GetShardedClusters()

	shardRSNames := map[string]bool{}
	for _, sc := range shardedClusters {
		for _, sh := range sc.Shards() {
			shardRSNames[sh.Id()] = true
		}
	}

	independentRSCount := 0
	for _, rs := range d.GetReplicaSets() {
		if !shardRSNames[rs.Name()] {
			independentRSCount++
		}
	}

	return len(shardedClusters) + independentRSCount
}

func checkReplicaSetsExist(d om.Deployment) []ValidationResult {
	if len(d.GetReplicaSets()) == 0 {
		return []ValidationResult{{
			Severity: SeverityError,
			Message:  "no replicaSets found; only replica set deployments can be migrated",
		}}
	}
	return nil
}

func checkReplicaSetProtocolVersion(d om.Deployment) []ValidationResult {
	var results []ValidationResult
	for _, rs := range d.GetReplicaSets() {
		rsID := rs.Name()
		pv := rs.ProtocolVersion()
		if pv != "" && pv != defaultProtocolVersion {
			results = append(results, ValidationResult{
				Severity: SeverityError,
				Message:  fmt.Sprintf("replica set %q has protocolVersion=%q; only %q is supported", rsID, pv, defaultProtocolVersion),
			})
		}
	}
	return results
}

func checkMembersReferenceProcesses(d om.Deployment, processMap map[string]om.Process) []ValidationResult {
	replicaSets := d.GetReplicaSets()
	if len(replicaSets) == 0 {
		return nil
	}

	var results []ValidationResult
	for _, rs := range replicaSets {
		rsID := rs.Name()
		members := rs.Members()

		if len(members) == 0 {
			results = append(results, ValidationResult{
				Severity: SeverityError,
				Message:  fmt.Sprintf("replica set %q has no members; cannot migrate an empty replica set", rsID),
			})
			continue
		}

		for _, m := range members {
			host := m.Name()
			if _, ok := processMap[host]; !ok {
				results = append(results, ValidationResult{
					Severity: SeverityError,
					Message:  fmt.Sprintf("member %q (rs %q) references a process not found in automation config", host, rsID),
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
func checkHeterogeneousProcessConfig(d om.Deployment, processMap map[string]om.Process) []ValidationResult {
	replicaSets := d.GetReplicaSets()
	if len(replicaSets) == 0 {
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
		cfg := mdbv1.NewEmptyAdditionalMongodConfig()
		args := proc.Args()
		extractNetConfig(args, cfg)
		extractStorageConfig(args, cfg)
		extractReplicationConfig(args, cfg)
		extractGenericSections(args, cfg)
		extractNonDefaultTLSMode(args, cfg)
		allFlat = append(allFlat, maputil.ToFlatMap(cfg.ToMap()))
	}

	if len(allFlat) < 2 {
		return nil
	}

	var results []ValidationResult
	for _, key := range findInconsistentKeys(allFlat) {
		results = append(results, ValidationResult{
			Severity: SeverityWarning,
			Message:  fmt.Sprintf("field %q differs across replica set members and will be excluded from the CR; reconcile before migration", key),
		})
	}
	return results
}

// findInconsistentKeys returns a sorted list of dotted keys whose serialized
// values differ across the flat config maps. Keys that are absent in all maps
// or identical everywhere are excluded.
func findInconsistentKeys(allFlat []map[string]string) []string {
	seen := map[string]bool{}
	for _, flat := range allFlat {
		for k := range flat {
			seen[k] = true
		}
	}

	var inconsistent []string
	for _, key := range slices.Sorted(maps.Keys(seen)) {
		var refVal string
		refSet := false
		consistent := true
		for _, flat := range allFlat {
			val, exists := flat[key]
			if !refSet {
				refVal = val
				refSet = exists
				continue
			}
			if exists != refSet || val != refVal {
				consistent = false
				break
			}
		}
		if !consistent {
			inconsistent = append(inconsistent, key)
		}
	}
	return inconsistent
}

// checkVersionConsistency warns when members in the first replica set have
// different MongoDB versions or featureCompatibilityVersions. The generated
// CR uses the version from the first member, so mismatches should be reconciled.
func checkVersionConsistency(d om.Deployment, processMap map[string]om.Process) []ValidationResult {
	replicaSets := d.GetReplicaSets()
	if len(replicaSets) == 0 {
		return nil
	}

	members := replicaSets[0].Members()
	versions := map[string]bool{}
	fcvs := map[string]bool{}

	for _, m := range members {
		proc, ok := processMap[m.Name()]
		if !ok {
			continue
		}
		versions[proc.Version()] = true
		fcvs[proc.FeatureCompatibilityVersion()] = true
	}

	var results []ValidationResult
	if len(versions) > 1 {
		keys := slices.Sorted(maps.Keys(versions))
		results = append(results, ValidationResult{
			Severity: SeverityWarning,
			Message:  fmt.Sprintf("members have different MongoDB versions %v; CR will use the first member's version — reconcile before migration", keys),
		})
	}
	if len(fcvs) > 1 {
		keys := slices.Sorted(maps.Keys(fcvs))
		results = append(results, ValidationResult{
			Severity: SeverityWarning,
			Message:  fmt.Sprintf("members have different FCVs %v; CR will use the first member's FCV", keys),
		})
	}
	return results
}

// checkProcessConfigDrift warns when per-process logRotate or auditLogRotate
// in the AC differs from the project-level values returned by the OM API. The
// generated CR uses the project-level values, so any drift means the AC
// processes have stale or manually edited settings.
func checkProcessConfigDrift(d om.Deployment, processMap map[string]om.Process, projectProcessConfigs *ProjectProcessConfigs) []ValidationResult {
	if projectProcessConfigs == nil {
		return nil
	}

	replicaSets := d.GetReplicaSets()
	if len(replicaSets) == 0 {
		return nil
	}

	projectLogRotate, _ := maputil.StructToMap(projectProcessConfigs.SystemLogRotate)
	projectAuditLogRotate, _ := maputil.StructToMap(projectProcessConfigs.AuditLogRotate)

	var results []ValidationResult
	for _, m := range replicaSets[0].Members() {
		proc, ok := processMap[m.Name()]
		if !ok {
			continue
		}

		if len(projectLogRotate) > 0 {
			processLR := proc.GetLogRotate()
			if len(processLR) > 0 && !maputil.FlatMapsEqual(processLR, projectLogRotate) {
				results = append(results, ValidationResult{
					Severity: SeverityWarning,
					Message:  fmt.Sprintf("process %q logRotate differs from project-level config; CR will use the project-level value", proc.Name()),
				})
			}
		}

		if len(projectAuditLogRotate) > 0 {
			processALR := proc.GetAuditLogRotate()
			if len(processALR) > 0 && !maputil.FlatMapsEqual(processALR, projectAuditLogRotate) {
				results = append(results, ValidationResult{
					Severity: SeverityWarning,
					Message:  fmt.Sprintf("process %q auditLogRotate differs from project-level config; CR will use the project-level value", proc.Name()),
				})
			}
		}
	}
	return results
}

func checkMemberPreservedFields(d om.Deployment) []ValidationResult {
	var results []ValidationResult
	for _, rs := range d.GetReplicaSets() {
		rsID := rs.Name()

		for _, m := range rs.Members() {
			host := m.Name()

			if delay := m.SlaveDelay(); delay > 0 {
				results = append(results, ValidationResult{
					Severity: SeverityWarning,
					Message:  fmt.Sprintf("member %q (rs %q) has slaveDelay=%d; preserved while external, lost when operator-managed", host, rsID, delay),
				})
			}

			if m.IsHidden() {
				results = append(results, ValidationResult{
					Severity: SeverityWarning,
					Message:  fmt.Sprintf("member %q (rs %q) is hidden; preserved while external, lost when operator-managed", host, rsID),
				})
			}

		}
	}
	return results
}
