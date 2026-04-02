package migrate

import (
	"fmt"
	"maps"
	"os"
	"slices"

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

// ValidateMigration checks the automation config for operator compatibility.
// Structural errors gate further checks.
func ValidateMigration(ac *om.AutomationConfig, processMap map[string]om.Process, projectAgentConfigs *ProjectAgentConfigs, projectProcessConfigs *ProjectProcessConfigs) ([]ValidationResult, *om.Process) {
	var results []ValidationResult

	results = append(results, checkOneDeploymentPerProject(ac.Deployment)...)
	results = append(results, checkReplicaSetsExist(ac.Deployment)...)
	results = append(results, checkMembersReferenceProcesses(ac.Deployment, processMap)...)
	for _, r := range results {
		if r.Severity == SeverityError {
			return results, nil
		}
	}

	results = append(results, validateAuth(ac.Auth)...)
	results = append(results, validateScram(ac.Auth)...)
	results = append(results, validateX509(ac.Auth)...)
	results = append(results, validateTLS(ac.AgentSSL)...)
	results = append(results, validateAgentConfig(projectAgentConfigs)...)
	results = append(results, validateLDAP(ac.Ldap)...)
	results = append(results, validateProjectOptions(ac.Deployment)...)
	results = append(results, validateProcessConfig(ac.Deployment, processMap)...)
	results = append(results, checkReplicaSetProtocolVersion(ac.Deployment)...)
	results = append(results, checkVersionConsistency(ac.Deployment, processMap)...)
	results = append(results, checkMemberPreservedFields(ac.Deployment)...)
	for _, r := range results {
		if r.Severity == SeverityError {
			return results, nil
		}
	}
	members := ac.Deployment.GetReplicaSets()[0].Members()
	sourceProcess := pickSourceProcess(members, processMap)
	results = append(results, checkDifferentMongodConfig(members, processMap, sourceProcess)...)
	results = append(results, checkProcessConfigDrift(members, processMap, projectProcessConfigs)...)

	return results, sourceProcess
}

// pickSourceProcess returns the first voting+priority member's process, falling back to members[0].
func pickSourceProcess(members []om.ReplicaSetMember, processMap map[string]om.Process) *om.Process {
	m := members[0]
	for _, candidate := range members {
		if candidate.Votes() > 0 && candidate.Priority() > 0 {
			m = candidate
			break
		}
	}
	fmt.Fprintf(os.Stderr, "spec.additionalMongodConfig and spec.agent.mongod.systemLog will be taken from process %q. Review all members and reconcile any differences before migration.\n", m.Name())
	proc := processMap[m.Name()]
	return &proc
}

// validateAuth checks autoUser, keyFile, and keyFileWindows against operator defaults.
func validateAuth(auth *om.Auth) []ValidationResult {
	if auth == nil || auth.Disabled {
		return nil
	}

	var results []ValidationResult

	if auth.AutoUser == "" {
		results = append(results, ValidationResult{
			Severity: SeverityError,
			Message:  "auth.autoUser is empty. The operator requires an automation agent user when authentication is enabled.",
		})
	}
	if auth.KeyFile != "" && auth.KeyFile != defaultKeyFile {
		results = append(results, ValidationResult{
			Severity: SeverityError,
			Message:  fmt.Sprintf("auth.keyFile %q differs from the operator default %q. This value is not configurable via the Custom Resource.", auth.KeyFile, defaultKeyFile),
		})
	}
	if auth.KeyFileWindows != "" && auth.KeyFileWindows != defaultKeyFileWindows {
		results = append(results, ValidationResult{
			Severity: SeverityError,
			Message:  fmt.Sprintf("auth.keyFileWindows %q differs from the operator default %q. This value is not configurable via the Custom Resource.", auth.KeyFileWindows, defaultKeyFileWindows),
		})
	}

	return results
}

// validateScram checks the autoUser has a matching usersWanted entry for SCRAM.
func validateScram(auth *om.Auth) []ValidationResult {
	if auth == nil || auth.Disabled || auth.AutoUser == "" {
		return nil
	}
	switch auth.AutoAuthMechanism {
	case "SCRAM-SHA-256", "SCRAM-SHA-1", "MONGODB-CR":
	default:
		return nil
	}
	hasMatchingUser := slices.ContainsFunc(auth.Users, func(u *om.MongoDBUser) bool {
		return u != nil && u.Username == auth.AutoUser && u.Database == util.DefaultUserDatabase
	})
	if !hasMatchingUser {
		return []ValidationResult{{
			Severity: SeverityError,
			Message:  fmt.Sprintf("auth.autoUser %q has no matching entry in auth.usersWanted (database: %q). Agent authentication will fail after migration.", auth.AutoUser, util.DefaultUserDatabase),
		}}
	}
	return nil
}

// validateTLS checks project-level TLS paths against operator defaults.
func validateTLS(agentSSL *om.AgentSSL) []ValidationResult {
	if agentSSL == nil {
		return nil
	}

	var results []ValidationResult

	if agentSSL.AutoPEMKeyFilePath != "" {
		results = append(results, ValidationResult{
			Severity: SeverityError,
			Message:  fmt.Sprintf("tls.autoPEMKeyFilePath %q will be overwritten by the operator during reconciliation.", agentSSL.AutoPEMKeyFilePath),
		})
	}
	if agentSSL.CAFilePath != "" && agentSSL.CAFilePath != defaultCAFilePath {
		results = append(results, ValidationResult{
			Severity: SeverityError,
			Message:  fmt.Sprintf("tls.CAFilePath %q differs from the operator default %q and will be overwritten.", agentSSL.CAFilePath, defaultCAFilePath),
		})
	}
	if agentSSL.CAFilePath != "" {
		results = append(results, ValidationResult{
			Severity: SeverityWarning,
			Message:  "TLS is enabled. Create a kubernetes.io/tls Secret named \"<certsSecretPrefix>-<resourceName>-cert\" with keys \"tls.crt\" and \"tls.key\" before applying the Custom Resource.",
		})
	}

	return results
}

// validateX509 warns when MONGODB-X509 agent auth is configured.
func validateX509(auth *om.Auth) []ValidationResult {
	if auth == nil || auth.Disabled {
		return nil
	}
	if auth.AutoAuthMechanism != "MONGODB-X509" {
		return nil
	}
	return []ValidationResult{{
		Severity: SeverityWarning,
		Message:  "MONGODB-X509 agent authentication is configured. Create a kubernetes.io/tls Secret named \"<certsSecretPrefix>-<resourceName>-agent-certs\" with keys \"tls.crt\" and \"tls.key\" before applying the Custom Resource.",
	}}
}

// validateAgentConfig checks agent log paths against operator defaults.
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
				Message:  fmt.Sprintf("monitoringAgentConfig.logPath %q differs from the operator default %q. This value is not configurable via the Custom Resource.", logPath, defaultMonitoringAgentLogPath),
			})
		}
	}

	if configs.BackupConfig != nil {
		logPath := configs.BackupConfig.LogPath()
		if logPath != "" && logPath != defaultBackupAgentLogPath {
			results = append(results, ValidationResult{
				Severity: SeverityError,
				Message:  fmt.Sprintf("backupAgentConfig.logPath %q differs from the operator default %q. This value is not configurable via the Custom Resource.", logPath, defaultBackupAgentLogPath),
			})
		}
	}

	return results
}

// validateLDAP checks bindMethod is "simple" and warns when a CA certificate is present.
func validateLDAP(l *ldap.Ldap) []ValidationResult {
	if l == nil {
		return nil
	}

	var results []ValidationResult

	if l.BindMethod != "" && l.BindMethod != "simple" {
		results = append(results, ValidationResult{
			Severity: SeverityError,
			Message:  fmt.Sprintf("LDAP bindMethod %q will be overwritten by the operator default \"simple\" after migration.", l.BindMethod),
		})
	}
	if l.CaFileContents != "" {
		results = append(results, ValidationResult{
			Severity: SeverityWarning,
			Message:  "LDAP CA certificate is present. Create ConfigMap \"ldap-ca\" with key \"ca.pem\" before applying the Custom Resource.",
		})
	}

	return results
}

// validateProjectOptions checks options.downloadBase against the operator default.
func validateProjectOptions(d om.Deployment) []ValidationResult {
	downloadBase := d.DownloadBase()
	if downloadBase != defaultDownloadBase {
		return []ValidationResult{{
			Severity: SeverityError,
			Message:  fmt.Sprintf("options.downloadBase %q differs from the operator default %q. This value is not configurable via the Custom Resource.", downloadBase, defaultDownloadBase),
		}}
	}
	return nil
}

// validateProcessConfig runs per-process checks.
func validateProcessConfig(d om.Deployment, processMap map[string]om.Process) []ValidationResult {
	var results []ValidationResult

	results = append(results, checkProcessesAreValid(d)...)
	results = append(results, checkAuthSchemaVersion(d)...)
	results = append(results, checkNonDefaultDbPath(d, processMap)...)
	results = append(results, checkTLS(d)...)
	results = append(results, checkTLSPaths(d)...)
	results = append(results, checkShardingClusterRole(d)...)

	return results
}

func checkAuthSchemaVersion(d om.Deployment) []ValidationResult {
	var results []ValidationResult
	for _, proc := range d.GetProcesses() {
		asv := proc.AuthSchemaVersion()
		if asv != defaultAuthSchemaVersion {
			results = append(results, ValidationResult{
				Severity: SeverityError,
				Message:  fmt.Sprintf("Process %q has authSchemaVersion %d. The operator default is %d.", proc.Name(), asv, defaultAuthSchemaVersion),
			})
		}
	}
	return results
}

func checkProcessesAreValid(d om.Deployment) []ValidationResult {
	processes := d.GetProcesses()
	if len(processes) == 0 {
		return []ValidationResult{{
			Severity: SeverityError,
			Message:  "The automation config contains no processes.",
		}}
	}

	var results []ValidationResult
	for _, proc := range processes {
		name := proc.Name()

		if proc.ProcessType() != om.ProcessTypeMongod {
			results = append(results, ValidationResult{
				Severity: SeverityError,
				Message:  fmt.Sprintf("Process %q has processType %q. Only mongod replica set processes are supported.", name, string(proc.ProcessType())),
			})
		}

		if proc.IsDisabled() {
			results = append(results, ValidationResult{
				Severity: SeverityWarning,
				Message:  fmt.Sprintf("Process %q is disabled and will be skipped.", name),
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
				Message:  fmt.Sprintf("Process %q has dbPath %q. The operator uses %q; the path will change when the member becomes operator-managed.", host, dbPath, util.PvcMountPathData),
			}}
		}
	}
	return nil
}

// checkTLS warns once when no process has TLS configured, so the user knows to set
// spec.additionalMongodConfig.net.tls.mode to "disabled" to match the operator default.
func checkTLS(d om.Deployment) []ValidationResult {
	var results []ValidationResult
	noTLSReported := false

	for _, proc := range d.GetProcesses() {
		args := proc.Args()

		if !noTLSReported {
			mode := pkgtls.GetTLSModeFromMongodConfig(args)
			if len(proc.NetTLSSections()) == 0 || mode == pkgtls.Disabled {
				results = append(results, ValidationResult{
					Severity: SeverityWarning,
					Message:  fmt.Sprintf("Process %q has no TLS. Set spec.additionalMongodConfig.net.tls.mode to \"disabled\" to match the operator and avoid a deployment change.", proc.Name()),
				})
				noTLSReported = true
			}
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
					Message:  fmt.Sprintf("Process %q has net.%s.certificateKeyFile %q; the operator defaults to %q. The certificate path will change after migration.", name, tlsKey, certKey, util.PEMKeyFilePathInContainer),
				})
			}
			if pemKey != "" && pemKey != util.PEMKeyFilePathInContainer {
				results = append(results, ValidationResult{
					Severity: SeverityError,
					Message:  fmt.Sprintf("Process %q has net.%s.PEMKeyFile %q. The operator default is %q.", name, tlsKey, pemKey, util.PEMKeyFilePathInContainer),
				})
			}

			expectedClusterFile := fmt.Sprintf("%s%s-pem", util.InternalClusterAuthMountPath, name)
			if clusterFile, _ := tlsSection["clusterFile"].(string); clusterFile != "" && clusterFile != expectedClusterFile {
				results = append(results, ValidationResult{
					Severity: SeverityError,
					Message:  fmt.Sprintf("Process %q has net.%s.clusterFile %q. The operator default is %q.", name, tlsKey, clusterFile, expectedClusterFile),
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
				Message:  fmt.Sprintf("Process %q has sharding.clusterRole %q. Only standalone replica sets are supported.", proc.Name(), role),
			})
		}
	}
	return results
}

func checkOneDeploymentPerProject(d om.Deployment) []ValidationResult {
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
	count := len(shardedClusters) + independentRSCount
	if count <= 1 {
		return nil
	}
	return []ValidationResult{{
		Severity: SeverityError,
		Message:  fmt.Sprintf("The project contains %d deployments. The operator requires exactly one deployment per project. Split the project before migrating.", count),
	}}
}

func checkReplicaSetProtocolVersion(d om.Deployment) []ValidationResult {
	var results []ValidationResult
	for _, rs := range d.GetReplicaSets() {
		rsID := rs.Name()
		pv := rs.ProtocolVersion()
		if pv != "" && pv != defaultProtocolVersion {
			results = append(results, ValidationResult{
				Severity: SeverityError,
				Message:  fmt.Sprintf("Replica set %q has protocolVersion %q. Only %q is supported.", rsID, pv, defaultProtocolVersion),
			})
		}
	}
	return results
}

func checkReplicaSetsExist(d om.Deployment) []ValidationResult {
	if len(d.GetReplicaSets()) == 0 {
		return []ValidationResult{{
			Severity: SeverityError,
			Message:  "No replica sets found. Only replica set deployments can be migrated.",
		}}
	}
	return nil
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
				Message:  fmt.Sprintf("Replica set %q has no members. An empty replica set cannot be migrated.", rsID),
			})
			continue
		}

		for _, m := range members {
			host := m.Name()
			if _, ok := processMap[host]; !ok {
				results = append(results, ValidationResult{
					Severity: SeverityError,
					Message:  fmt.Sprintf("Member %q (replica set %q) references a process that was not found in the automation config.", host, rsID),
				})
			}
		}
	}
	return results
}

// checkDifferentMongodConfig warns when members have additionalMongodConfig fields that differ from the source process.
func checkDifferentMongodConfig(members []om.ReplicaSetMember, processMap map[string]om.Process, sourceProcess *om.Process) []ValidationResult {
	if sourceProcess == nil || len(members) < 2 {
		return nil
	}

	sourceCfg := sourceProcess.AdditionalMongodConfig()
	var sourceFlat map[string]string
	if sourceCfg != nil {
		sourceFlat = maputil.ToFlatMap(sourceCfg.ToMap())
	}

	var results []ValidationResult
	for _, m := range members {
		proc, ok := processMap[m.Name()]
		if !ok || proc.Name() == sourceProcess.Name() {
			continue
		}
		var memberFlat map[string]string
		if cfg := proc.AdditionalMongodConfig(); cfg != nil {
			memberFlat = maputil.ToFlatMap(cfg.ToMap())
		}
		for _, key := range findInconsistentKeys([]map[string]string{sourceFlat, memberFlat}) {
			results = append(results, ValidationResult{
				Severity: SeverityWarning,
				Message:  fmt.Sprintf("Field %q on process %q differs from source process %q and will be excluded from the Custom Resource. Reconcile before migration.", key, proc.Name(), sourceProcess.Name()),
			})
		}
	}
	return results
}

// findInconsistentKeys returns keys whose values differ across the flat maps.
func findInconsistentKeys(allFlat []map[string]string) []string {
	seen := map[string]bool{}
	for _, flat := range allFlat {
		for k := range flat {
			seen[k] = true
		}
	}
	var inconsistent []string
	for _, key := range slices.Sorted(maps.Keys(seen)) {
		refVal, refExists := allFlat[0][key]
		consistent := true
		for _, flat := range allFlat[1:] {
			val, exists := flat[key]
			if exists != refExists || val != refVal {
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

// checkVersionConsistency warns when members have different MongoDB versions or feature compatibility versions.
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
		results = append(results, ValidationResult{
			Severity: SeverityWarning,
			Message:  fmt.Sprintf("Members have different MongoDB versions %v. The Custom Resource will use the first member's version. Reconcile before migration.", slices.Sorted(maps.Keys(versions))),
		})
	}
	if len(fcvs) > 1 {
		results = append(results, ValidationResult{
			Severity: SeverityWarning,
			Message:  fmt.Sprintf("Members have different feature compatibility versions %v. The Custom Resource will use the first member's FCV.", slices.Sorted(maps.Keys(fcvs))),
		})
	}
	return results
}

// checkProcessConfigDrift warns when per-process logRotate/auditLogRotate differs from project-level config.
func checkProcessConfigDrift(members []om.ReplicaSetMember, processMap map[string]om.Process, projectProcessConfigs *ProjectProcessConfigs) []ValidationResult {
	if projectProcessConfigs == nil {
		return nil
	}

	projectLogRotate, _ := maputil.StructToMap(projectProcessConfigs.SystemLogRotate)
	projectAuditLogRotate, _ := maputil.StructToMap(projectProcessConfigs.AuditLogRotate)

	var results []ValidationResult
	for _, m := range members {
		proc, ok := processMap[m.Name()]
		if !ok {
			continue
		}

		if len(projectLogRotate) > 0 {
			processLR := proc.GetLogRotate()
			if len(processLR) > 0 && !maputil.FlatMapsEqual(processLR, projectLogRotate) {
				results = append(results, ValidationResult{
					Severity: SeverityWarning,
					Message:  fmt.Sprintf("Process %q logRotate differs from project-level config. The Custom Resource will use the project-level value.", proc.Name()),
				})
			}
		}

		if len(projectAuditLogRotate) > 0 {
			processALR := proc.GetAuditLogRotate()
			if len(processALR) > 0 && !maputil.FlatMapsEqual(processALR, projectAuditLogRotate) {
				results = append(results, ValidationResult{
					Severity: SeverityWarning,
					Message:  fmt.Sprintf("Process %q auditLogRotate differs from project-level config. The Custom Resource will use the project-level value.", proc.Name()),
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
					Message:  fmt.Sprintf("Member %q (replica set %q) has slaveDelay %d. This is preserved while the member is external and is lost when operator-managed.", host, rsID, delay),
				})
			}

			if m.IsHidden() {
				results = append(results, ValidationResult{
					Severity: SeverityWarning,
					Message:  fmt.Sprintf("Member %q (replica set %q) is hidden. This is preserved while the member is external and is lost when operator-managed.", host, rsID),
				})
			}

		}
	}
	return results
}
