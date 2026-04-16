package migrate

import (
	"fmt"
	"reflect"
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
func ValidateMigration(ac *om.AutomationConfig, processMap map[string]om.Process, projectConfigs *ProjectConfigs) ([]ValidationResult, *om.Process) {
	var results []ValidationResult

	results = append(results, validateOneDeploymentPerProject(ac.Deployment)...)
	results = append(results, validateReplicaSetsExist(ac.Deployment)...)
	results = append(results, validateMembersReferenceProcesses(ac.Deployment, processMap)...)
	for _, r := range results {
		if r.Severity == SeverityError {
			return results, nil
		}
	}

	results = append(results, validateAuth(ac.Auth)...)
	results = append(results, validateScram(ac.Auth)...)
	results = append(results, validateX509(ac.Auth)...)
	results = append(results, validateAgentTLS(ac.AgentSSL)...)
	results = append(results, validateAgentConfig(projectConfigs)...)
	results = append(results, validateLDAP(ac.Ldap)...)
	results = append(results, validateProjectOptions(ac.Deployment)...)
	results = append(results, validateReplicaSetProtocolVersion(ac.Deployment)...)
	results = append(results, validateMemberPreservedFields(ac.Deployment)...)
	for _, r := range results {
		if r.Severity == SeverityError {
			return results, nil
		}
	}

	replicaSets := ac.Deployment.GetReplicaSets()
	if len(replicaSets) == 0 {
		return results, nil
	}
	members := replicaSets[0].Members()
	sourceProcess, err := pickSourceProcess(members, processMap)
	if err != nil {
		return append(results, ValidationResult{Severity: SeverityError, Message: err.Error()}), nil
	}
	results = append(results, ValidationResult{
		Severity: SeverityWarning,
		Message:  fmt.Sprintf("spec.additionalMongodConfig and spec.agent.mongod.systemLog will be taken from process %q. Review all members and reconcile any differences before migration.", sourceProcess.Name()),
	})
	results = append(results, validateProcessConfig(ac.Deployment, processMap, sourceProcess, projectConfigs)...)

	return results, sourceProcess
}

// pickSourceProcess returns the first voting+priority member's process, or an error if none qualify.
func pickSourceProcess(members []om.ReplicaSetMember, processMap map[string]om.Process) (*om.Process, error) {
	for _, candidate := range members {
		if candidate.Votes() > 0 && candidate.Priority() > 0 {
			proc, ok := processMap[candidate.Name()]
			if !ok {
				continue
			}
			return &proc, nil
		}
	}
	return nil, fmt.Errorf("no voting+priority member found in replica set; cannot determine source process")
}

// validateOneDeploymentPerProject ensures the project has exactly one replica set.
// Sharded cluster support will be added in a follow-up PR.
func validateOneDeploymentPerProject(d om.Deployment) []ValidationResult {
	if len(d.GetShardedClusters()) > 0 {
		return []ValidationResult{{
			Severity: SeverityError,
			Message:  "Sharded cluster migration is not yet supported. Only replica set deployments can be migrated.",
		}}
	}
	count := len(d.GetReplicaSets())
	if count <= 1 {
		return nil
	}
	return []ValidationResult{{
		Severity: SeverityError,
		Message:  fmt.Sprintf("The project contains %d deployments. The operator requires exactly one deployment per project. Split the project before migrating.", count),
	}}
}

func validateReplicaSetsExist(d om.Deployment) []ValidationResult {
	if len(d.GetReplicaSets()) == 0 {
		return []ValidationResult{{
			Severity: SeverityError,
			Message:  "No replica sets found. Only replica set and sharded cluster deployments can be migrated.",
		}}
	}
	return nil
}

func validateMembersReferenceProcesses(d om.Deployment, processMap map[string]om.Process) []ValidationResult {
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

// validateAgentTLS checks project-level TLS paths against operator defaults.
func validateAgentTLS(agentSSL *om.AgentSSL) []ValidationResult {
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

// validateAgentConfig checks agent log paths against operator defaults.
func validateAgentConfig(configs *ProjectConfigs) []ValidationResult {
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
			Message:  fmt.Sprintf("LDAP bindMethod %q is not supported by the operator. Only \"simple\" is supported", l.BindMethod),
		})
	}
	if l.CaFileContents != "" {
		results = append(results, ValidationResult{
			Severity: SeverityWarning,
			Message:  "LDAP CA certificate is present. The tool will create ConfigMap \"ldap-ca\" with key \"ca.pem\" automatically (or include it in the output when --dry-run is set).",
		})
	}

	return results
}

// validateProjectOptions checks options.downloadBase against the operator default.
func validateProjectOptions(d om.Deployment) []ValidationResult {
	downloadBase := d.DownloadBase()
	if downloadBase == "" || downloadBase == defaultDownloadBase {
		return nil
	}
	return []ValidationResult{{
		Severity: SeverityError,
		Message:  fmt.Sprintf("options.downloadBase %q differs from the operator default %q. This value is not configurable via the Custom Resource.", downloadBase, defaultDownloadBase),
	}}
}

func validateReplicaSetProtocolVersion(d om.Deployment) []ValidationResult {
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

func validateMemberPreservedFields(d om.Deployment) []ValidationResult {
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

// validateProcessConfig runs per-process checks against the deployment and source process.
func validateProcessConfig(d om.Deployment, processMap map[string]om.Process, sourceProcess *om.Process, projectProcessConfigs *ProjectConfigs) []ValidationResult {
	var results []ValidationResult

	results = append(results, validateProcessesAreValid(d)...)
	results = append(results, validateAuthSchemaVersion(d)...)
	results = append(results, validateNonDefaultDbPath(d, processMap)...)
	results = append(results, validateTLS(sourceProcess)...)
	results = append(results, validateTLSPaths(d)...)
	results = append(results, validateProcessConfigDrift(sourceProcess, projectProcessConfigs)...)

	return results
}

// validateProcessesAreValid checks that all processes are mongod or mongos and not disabled.
func validateProcessesAreValid(d om.Deployment) []ValidationResult {
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
		pt := proc.ProcessType()

		if pt != om.ProcessTypeMongod && pt != om.ProcessTypeMongos {
			results = append(results, ValidationResult{
				Severity: SeverityError,
				Message:  fmt.Sprintf("Process %q has processType %q. Only mongod and mongos processes are supported.", name, string(pt)),
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

// validateAuthSchemaVersion checks each mongod process has the expected authSchemaVersion.
func validateAuthSchemaVersion(d om.Deployment) []ValidationResult {
	var results []ValidationResult
	for _, proc := range d.GetProcesses() {
		if proc.ProcessType() == om.ProcessTypeMongos {
			continue
		}
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

func validateNonDefaultDbPath(d om.Deployment, processMap map[string]om.Process) []ValidationResult {
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

// validateTLS warns when TLS is absent so the user knows to set net.tls.mode to "disabled".
func validateTLS(proc *om.Process) []ValidationResult {
	if len(proc.NetTLSSections()) == 0 || pkgtls.GetTLSModeFromMongodConfig(proc.Args()) == pkgtls.Disabled {
		return []ValidationResult{{
			Severity: SeverityWarning,
			Message:  fmt.Sprintf("Process %q has no TLS. Set spec.additionalMongodConfig.net.tls.mode to \"disabled\" to match the operator and avoid a deployment change.", proc.Name()),
		}}
	}
	return nil
}

func validateTLSPaths(d om.Deployment) []ValidationResult {
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

// validateProcessConfigDrift warns when the source process logRotate/auditLogRotate differs from project-level config.
func validateProcessConfigDrift(sourceProcess *om.Process, projectProcessConfigs *ProjectConfigs) []ValidationResult {
	if projectProcessConfigs == nil {
		return nil
	}

	projectLogRotate, _ := maputil.StructToMap(projectProcessConfigs.SystemLogRotate)
	projectAuditLogRotate, _ := maputil.StructToMap(projectProcessConfigs.AuditLogRotate)

	var results []ValidationResult
	if len(projectLogRotate) > 0 {
		processLR := sourceProcess.GetLogRotate()
		if len(processLR) > 0 && !reflect.DeepEqual(processLR, projectLogRotate) {
			results = append(results, ValidationResult{
				Severity: SeverityWarning,
				Message:  fmt.Sprintf("Process %q logRotate differs from project-level config. The Custom Resource will use the project-level value.", sourceProcess.Name()),
			})
		}
	}

	if len(projectAuditLogRotate) > 0 {
		processALR := sourceProcess.GetAuditLogRotate()
		if len(processALR) > 0 && !reflect.DeepEqual(processALR, projectAuditLogRotate) {
			results = append(results, ValidationResult{
				Severity: SeverityWarning,
				Message:  fmt.Sprintf("Process %q auditLogRotate differs from project-level config. The Custom Resource will use the project-level value.", sourceProcess.Name()),
			})
		}
	}

	return results
}
