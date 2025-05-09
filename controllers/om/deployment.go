package om

import (
	"encoding/gob"
	"encoding/json"
	"fmt"
	"math"
	"regexp"

	"github.com/blang/semver"
	"github.com/spf13/cast"
	"go.uber.org/zap"
	"golang.org/x/xerrors"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	mdbcv1 "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/automationconfig"
	"github.com/mongodb/mongodb-kubernetes/pkg/tls"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/maputil"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/stringutil"
)

type DeploymentType int

const (
	// Note that the default version constants shouldn't need to be changed often
	// as the AutomationAgent upgrades both other agents automatically

	MonitoringAgentDefaultVersion = "11.12.0.7388-1"

	BackupAgentDefaultVersion = "11.12.0.7388-1"

	// Default listen address for Prometheus
	ListenAddress = "0.0.0.0"
)

func init() {
	// gob is used to implement a deep copy internally. If a data type is part of
	// a deep copy performed using util.MapDeepCopy—which includes anything used
	// as part of a "process" object embedded within a deployment—then it must be
	// registered below as otherwise the operator will successfully compile and
	// run but be completely broken.
	// TODO should we move this to main.go?
	gob.Register(map[string]interface{}{})
	gob.Register([]interface{}{})
	gob.Register(map[string]int{})
	gob.Register(map[string]string{})
	gob.Register([]interface{}{})
	gob.Register([]Process{})
	gob.Register([]ReplicaSet{})
	gob.Register([]ReplicaSetMember{})
	gob.Register([]ShardedCluster{})
	gob.Register([]MongoDbVersionConfig{})
	gob.Register([]Shard{})
	gob.Register(ProcessTypeMongos)
	gob.Register(mdbv1.MongoDBHorizonConfig{})

	gob.Register(tls.Require)
	gob.Register(tls.Prefer)
	gob.Register(tls.Allow)
	gob.Register(tls.Disabled)
	gob.Register([]mdbv1.MongoDbRole{})
	gob.Register([]automationconfig.MemberOptions{})
}

// Deployment is a map representing the automation agent's cluster configuration.
// For more information see the following documentation:
// https://docs.opsmanager.mongodb.com/current/reference/cluster-configuration/
// https://github.com/10gen/mms-automation/blob/master/go_planner/config_specs/clusterConfig_spec.md
//
// Dev note: it's important to keep to the following principle during development: we don't use structs for json
// (de)serialization as we don't want to own the schema and synchronize it with the api one constantly.
// Also, we don't want to override any configuration provided by OM by accident.
// The Operator only sets the configuration it "owns" but
// keeps the other one that was set by the user in Ops Manager if any
type Deployment map[string]interface{}

func BuildDeploymentFromBytes(jsonBytes []byte) (Deployment, error) {
	deployment := Deployment{}
	err := json.Unmarshal(jsonBytes, &deployment)
	return deployment, err
}

func NewDeployment() Deployment {
	ans := Deployment{}
	ans.setProcesses(make([]Process, 0))
	ans.setReplicaSets(make([]ReplicaSet, 0))
	ans.setShardedClusters(make([]ShardedCluster, 0))
	ans.setMonitoringVersions(make([]interface{}, 0))
	ans.setBackupVersions(make([]interface{}, 0))

	// these keys are required to exist for mergo to merge
	// correctly
	ans["auth"] = make(map[string]interface{})
	ans["tls"] = map[string]interface{}{
		"clientCertificateMode": util.OptionalClientCertficates,
		"CAFilePath":            util.CAFilePathInContainer,
	}
	return ans
}

// TLSConfigurationWillBeDisabled checks that if applying this security configuration the Deployment
// TLS configuration will go from Enabled -> Disabled.
func (d Deployment) TLSConfigurationWillBeDisabled(security *mdbv1.Security) bool {
	tlsIsCurrentlyEnabled := false

	// To detect that TLS is enabled, it is sufficient to check for the
	// d["tls"]["CAFilePath"] attribute to have a value different from nil.
	if tlsMap, ok := d["tls"]; ok {
		if caFilePath, ok := tlsMap.(map[string]interface{})["CAFilePath"]; ok {
			if caFilePath != nil {
				tlsIsCurrentlyEnabled = true
			}
		}
	}

	return tlsIsCurrentlyEnabled && !security.IsTLSEnabled()
}

// ConfigureTLS configures the deployment's TLS settings from the security
// specification provided by the user in the mongodb resource.
func (d Deployment) ConfigureTLS(security *mdbv1.Security, caFilePath string) {
	if !security.IsTLSEnabled() {
		d["tls"] = map[string]any{"clientCertificateMode": string(automationconfig.ClientCertificateModeOptional)}
		return
	}

	tlsConfig := util.ReadOrCreateMap(d, "tls")
	// ClientCertificateMode detects if Ops Manager requires client certification - may be there will be no harm
	// setting this to "REQUIRED" always (need to check). Otherwise, this should be configurable
	// see OM configurations that affects this setting from AA side:
	// https://docs.opsmanager.mongodb.com/current/reference/configuration/#mms.https.ClientCertificateMode
	// sslConfig["ClientCertificateMode"] = "OPTIONAL"
	// sslConfig["AutoPEMKeyFilePath"] = util.PEMKeyFilePathInContainer

	tlsConfig["CAFilePath"] = caFilePath
}

// MergeStandalone merges "operator" standalone ('standaloneMongo') to "OM" deployment ('d'). If we found the process
// with the same name - update some fields there. Otherwise, add the new one
func (d Deployment) MergeStandalone(standaloneMongo Process, specArgs26, prevArgs26 map[string]interface{}, l *zap.SugaredLogger) {
	if l == nil {
		l = zap.S()
	}
	log := l.With("standalone", standaloneMongo)

	// merging process in case exists, otherwise adding it
	for _, pr := range d.getProcesses() {
		if pr.Name() == standaloneMongo.Name() {
			pr.mergeFrom(standaloneMongo, specArgs26, prevArgs26)
			log.Debug("Merged process into existing one")
			return
		}
	}
	d.addProcess(standaloneMongo)
	log.Debug("Added process as current OM deployment didn't have it")
}

// MergeReplicaSet merges the "operator" replica set and its members to the "OM" deployment ("d"). If "alien" RS members are
// removed after merge - corresponding processes are removed as well.
func (d Deployment) MergeReplicaSet(operatorRs ReplicaSetWithProcesses, specArgs26, prevArgs26 map[string]interface{}, l *zap.SugaredLogger) {
	log := l.With("replicaSet", operatorRs.Rs.Name())

	r := d.getReplicaSetByName(operatorRs.Rs.Name())
	// If the new replica set is bigger than old one - we need to copy first member to positions of new members so that
	// they were merged with operator replica sets on next step
	// (in case OM made any changes to existing processes - these changes must be propagated to new members).
	if r != nil && len(operatorRs.Rs.Members()) > len(r.Members()) {
		if err := d.copyFirstProcessToNewPositions(operatorRs.Processes, len(r.Members()), l); err != nil {
			// I guess this error is not so serious to fail the whole process - RS will be scaled up anyway
			log.Error("Failed to copy first process (so new replica set processes may miss Ops Manager changes done to "+
				"existing replica set processes): %s", err)
		}
	}

	// Merging all RS processes
	for _, p := range operatorRs.Processes {
		d.MergeStandalone(p, specArgs26, prevArgs26, log)
	}

	if r == nil {
		// Adding a new Replicaset
		d.addReplicaSet(operatorRs.Rs)
		log.Debugw("Added replica set as current OM deployment didn't have it")
	} else {

		processesToRemove := r.mergeFrom(operatorRs.Rs)
		log.Debugw("Merged replica set into existing one")

		if len(processesToRemove) > 0 {
			d.removeProcesses(processesToRemove, log)
			log.Debugw("Removed processes as they were removed from replica set", "processesToRemove", processesToRemove)
		}
	}

	// In both cases (the new replicaset was added to OM deployment, or it was merged with OM one) we need to make sure
	// there are no more than 7 voting members
	d.limitVotingMembers(operatorRs.Rs.Name())
}

// ConfigurePrometheus adds Prometheus configuration to `Deployment` resource.
//
// If basic auth is enabled, then `hash` and `salt` need to be calculated by caller and passed in.
func (d Deployment) ConfigurePrometheus(prom *mdbcv1.Prometheus, hash string, salt string, certName string) automationconfig.Prometheus {
	if prom == nil {
		// No prometheus configuration this time
		return automationconfig.Prometheus{}
	}

	promConfig := automationconfig.NewDefaultPrometheus(prom.Username)

	if prom.TLSSecretRef.Name != "" {
		promConfig.TLSPemPath = util.SecretVolumeMountPathPrometheus + "/" + certName
		promConfig.Scheme = "https"
	} else {
		promConfig.TLSPemPath = ""
		promConfig.Scheme = "http"
	}

	promConfig.PasswordHash = hash
	promConfig.PasswordSalt = salt

	if prom.Port > 0 {
		promConfig.ListenAddress = fmt.Sprintf("%s:%d", ListenAddress, prom.Port)
	}

	if prom.MetricsPath != "" {
		promConfig.MetricsPath = prom.MetricsPath
	}

	d["prometheus"] = promConfig

	return promConfig
}

// DeploymentShardedClusterMergeOptions contains all the required values to update the ShardedCluster
// in the automation config. These values should be provided my the MongoDB resource.
type DeploymentShardedClusterMergeOptions struct {
	Name                                 string
	MongosProcesses                      []Process
	ConfigServerRs                       ReplicaSetWithProcesses
	Shards                               []ReplicaSetWithProcesses
	Finalizing                           bool
	ConfigServerAdditionalOptionsDesired map[string]interface{}
	MongosAdditionalOptionsDesired       map[string]interface{}
	ShardAdditionalOptionsDesired        map[string]interface{}
	ConfigServerAdditionalOptionsPrev    map[string]interface{}
	MongosAdditionalOptionsPrev          map[string]interface{}
	ShardAdditionalOptionsPrev           map[string]interface{}
}

// MergeShardedCluster merges "operator" sharded cluster into "OM" deployment ("d"). Mongos, config servers and all shards
// are all merged one by one.
// 'shardsToRemove' is an array containing names of shards which should be removed.
func (d Deployment) MergeShardedCluster(opts DeploymentShardedClusterMergeOptions) (bool, error) {
	log := zap.S().With("sharded cluster", opts.Name)

	err := d.mergeMongosProcesses(opts, log)
	if err != nil {
		return false, err
	}

	d.mergeConfigReplicaSet(opts, log)

	shardsScheduledForRemoval := d.mergeShards(opts, log)

	return shardsScheduledForRemoval, nil
}

// AddMonitoringAndBackup adds monitoring and backup agents to each process
// The automation agent will update the agents versions to the latest version automatically
// Note, that these two are deliberately combined as all clients (standalone, rs etc.) need both backup and monitoring
// together
func (d Deployment) AddMonitoringAndBackup(log *zap.SugaredLogger, tls bool, caFilepath string) {
	if len(d.getProcesses()) == 0 {
		return
	}
	d.AddMonitoring(log, tls, caFilepath)
	d.addBackup(log)
}

// DEPRECATED: this shouldn't be used as it may panic because of different underlying type; use getReplicaSets instead
func (d Deployment) ReplicaSets() []ReplicaSet {
	return d["replicaSets"].([]ReplicaSet)
}

func (d Deployment) GetReplicaSetByName(name string) ReplicaSet {
	for _, rs := range d.getReplicaSets() {
		if rs.Name() == name {
			return rs
		}
	}
	return nil
}

// AddMonitoring adds monitoring agents for all processes in the deployment
func (d Deployment) AddMonitoring(log *zap.SugaredLogger, tls bool, caFilePath string) {
	if len(d.getProcesses()) == 0 {
		return
	}
	monitoringVersions := d.getMonitoringVersions()
	for _, p := range d.getProcesses() {
		found := false
		var monitoringVersion map[string]interface{}
		for _, m := range monitoringVersions {
			monitoringVersion = m.(map[string]interface{})
			if monitoringVersion["hostname"] == p.HostName() {
				found = true
				break
			}
		}

		if !found {
			monitoringVersion = map[string]interface{}{
				"hostname": p.HostName(),
				"name":     MonitoringAgentDefaultVersion,
			}
			log.Debugw("Added monitoring agent configuration", "host", p.HostName(), "tls", tls)
			monitoringVersions = append(monitoringVersions, monitoringVersion)
		}

		monitoringVersion["hostname"] = p.HostName()

		if tls {
			additionalParams := map[string]string{
				"useSslForAllConnections":      "true",
				"sslTrustedServerCertificates": caFilePath,
			}

			pemKeyFile := p.EnsureTLSConfig()["PEMKeyFile"]
			if pemKeyFile != nil {
				additionalParams["sslClientCertificate"] = pemKeyFile.(string)
			}

			monitoringVersion["additionalParams"] = additionalParams
		}

	}
	d.setMonitoringVersions(monitoringVersions)
}

// RemoveMonitoringAndBackup removes both monitoring and backup agent configurations. This must be called when the
// Mongodb resource is being removed, otherwise UI will show non-existing agents in the "servers" tab
func (d Deployment) RemoveMonitoringAndBackup(names []string, log *zap.SugaredLogger) {
	d.removeMonitoring(names)
	d.removeBackup(names, log)
}

func (d Deployment) DisableProcesses(processNames []string) {
	for _, p := range processNames {
		d.getProcessByName(p).SetDisabled(true)
	}
}

func (d Deployment) MarkRsMembersUnvoted(rsName string, rsMembers []string) error {
	rs := d.getReplicaSetByName(rsName)
	if rs == nil {
		return xerrors.New("Failed to find Replica Set " + rsName)
	}

	failedMembers := ""
	for _, m := range rsMembers {
		rsMember := rs.findMemberByName(m)
		if rsMember == nil {
			failedMembers += m
		} else {
			rsMember.setVotes(0).setPriority(0)
		}
	}
	if failedMembers != "" {
		return xerrors.Errorf("failed to find the following members of Replica Set %s: %v", rsName, failedMembers)
	}
	return nil
}

// RemoveProcessByName removes the process from deployment
// Note, that the backup and monitoring configs are also cleaned up
func (d Deployment) RemoveProcessByName(name string, log *zap.SugaredLogger) error {
	s := d.getProcessByName(name)
	if s == nil {
		return xerrors.Errorf("Standalone %s does not exist", name)
	}

	d.removeProcesses([]string{s.Name()}, log)

	return nil
}

// RemoveReplicaSetByName removes replica set and all relevant processes from deployment
// Note, that the backup and monitoring configs are also cleaned up
func (d Deployment) RemoveReplicaSetByName(name string, log *zap.SugaredLogger) error {
	rs := d.getReplicaSetByName(name)
	if rs == nil {
		return xerrors.New("ReplicaSet does not exist")
	}

	currentRs := d.getReplicaSets()
	toKeep := make([]ReplicaSet, len(currentRs)-1)
	i := 0
	for _, el := range currentRs {
		if el.Name() != name {
			toKeep[i] = el
			i++
		}
	}

	d.setReplicaSets(toKeep)

	members := rs.Members()
	processNames := make([]string, len(members))
	for i, el := range members {
		processNames[i] = el.Name()
	}
	d.removeProcesses(processNames, log)

	return nil
}

// RemoveShardedClusterByName removes the sharded cluster element, all relevant replica sets and all processes.
// Note, that the backup and monitoring configs are also cleaned up
func (d Deployment) RemoveShardedClusterByName(clusterName string, log *zap.SugaredLogger) error {
	sc := d.getShardedClusterByName(clusterName)
	if sc == nil {
		return xerrors.New("sharded Cluster does not exist")
	}

	// 1. Remove the sharded cluster
	toKeep := make([]ShardedCluster, 0)
	for _, el := range d.getShardedClusters() {
		if el.Name() != clusterName {
			toKeep = append(toKeep, el)
		}
	}

	d.setShardedClusters(toKeep)

	// 2. Remove all replicasets and their processes for shards
	shards := sc.shards()
	shardNames := make([]string, len(shards))
	for _, el := range shards {
		shardNames = append(shardNames, el.id())
	}
	d.removeReplicaSets(shardNames, log)

	// 3. Remove config server replicaset
	_ = d.RemoveReplicaSetByName(sc.ConfigServerRsName(), log)

	// 4. Remove mongos processes for cluster
	d.removeProcesses(d.getMongosProcessesNames(clusterName), log)

	return nil
}

// GetProcessNames returns an array of all the process names relevant to the given deployment
// these processes are the only ones checked for goal state when updating the
// deployment
func (d Deployment) GetProcessNames(kind interface{}, name string) []string {
	switch kind.(type) {
	case ShardedCluster:
		return d.getShardedClusterProcessNames(name)
	case ReplicaSet:
		return d.getReplicaSetProcessNames(name)
	case Standalone:
		return []string{name}
	default:
		panic(xerrors.Errorf("unexpected kind: %v", kind))
	}
}

// ConfigureInternalClusterAuthentication configures all processes in processNames to have the corresponding
// clusterAuthenticationMode enabled
func (d Deployment) ConfigureInternalClusterAuthentication(processNames []string, clusterAuthenticationMode string, internalClusterPath string) {
	for _, p := range processNames {
		if process := d.getProcessByName(p); process != nil {
			process.ConfigureClusterAuthMode(clusterAuthenticationMode, internalClusterPath)
		}
	}
}

// GetInternalClusterFilePath returns the first InternalClusterFilepath for the given list of processes.
func (d Deployment) GetInternalClusterFilePath(processNames []string) string {
	for _, p := range processNames {
		if process := d.getProcessByName(p); process != nil {
			if !process.IsTLSEnabled() {
				return ""
			}
			tlsConf := process.EnsureTLSConfig()
			if v, ok := tlsConf["clusterFile"]; ok {
				return v.(string)
			}
		}
	}
	return ""
}

// SetInternalClusterFilePathOnlyIfItThePathHasChanged sets the internal cluster path for the given process names only if it has changed and has been set before.
func (d Deployment) SetInternalClusterFilePathOnlyIfItThePathHasChanged(names []string, filePath string, clusterAuth string, isRecovering bool) {
	if currPath := d.GetInternalClusterFilePath(names); isRecovering || currPath != filePath && currPath != "" {
		d.ConfigureInternalClusterAuthentication(names, clusterAuth, filePath)
	}
}

// MinimumMajorVersion returns the lowest major version in the entire deployment.
// this includes feature compatibility version. This can be used to determine
// which version of SCRAM-SHA the deployment can enable.
func (d Deployment) MinimumMajorVersion() uint64 {
	if len(d.getProcesses()) == 0 {
		return 0
	}
	minimumMajorVersion := semver.Version{Major: math.MaxUint64}
	for _, p := range d.getProcesses() {
		if p.FeatureCompatibilityVersion() != "" {
			fcv := fmt.Sprintf("%s.0", util.StripEnt(p.FeatureCompatibilityVersion()))
			semverFcv, _ := semver.Make(fcv)
			if semverFcv.LE(minimumMajorVersion) {
				minimumMajorVersion = semverFcv
			}
		} else {
			semverVersion, _ := semver.Make(util.StripEnt(p.Version()))
			if semverVersion.LE(minimumMajorVersion) {
				minimumMajorVersion = semverVersion
			}
		}
	}

	return minimumMajorVersion.Major
}

// allProcessesAreTLSEnabled ensures that every process in the given deployment is TLS enabled
// it is not possible to enable x509 authentication at the project level if a single process
// does not have TLS enabled.
func (d Deployment) AllProcessesAreTLSEnabled() bool {
	for _, p := range d.getProcesses() {
		if !p.IsTLSEnabled() {
			return false
		}
	}
	return true
}

func (d Deployment) GetAllHostnames() []string {
	hostnames := make([]string, d.NumberOfProcesses())
	for idx, p := range d.getProcesses() {
		hostnames[idx] = p.HostName()
	}

	return hostnames
}

func (d Deployment) NumberOfProcesses() int {
	return len(d.getProcesses())
}

// anyProcessHasInternalClusterAuthentication determines if at least one process
// has internal cluster authentication enabled. If this is true, it is impossible to disable
// x509 authentication
func (d Deployment) AnyProcessHasInternalClusterAuthentication() bool {
	return d.processesHaveInternalClusterAuthentication(d.getProcesses())
}

func (d Deployment) ExistingProcessesHaveInternalClusterAuthentication(processes []Process) bool {
	deploymentProcesses := make([]Process, 0)
	for _, p := range processes {
		deploymentProcess := d.getProcessByName(p.Name())
		if deploymentProcess != nil {
			deploymentProcesses = append(deploymentProcesses, *deploymentProcess)
		}
	}
	return d.processesHaveInternalClusterAuthentication(deploymentProcesses)
}

func (d Deployment) Serialize() ([]byte, error) {
	return json.Marshal(d)
}

// ToCanonicalForm performs serialization/deserialization to get a map without struct members
// This may be useful if the Operator version of Deployment (which may contain structs) needs to be compared with
// a deployment deserialized from json
func (d Deployment) ToCanonicalForm() Deployment {
	bytes, err := d.Serialize()
	if err != nil {
		// dev error
		panic(err)
	}
	var canonical Deployment
	canonical, err = BuildDeploymentFromBytes(bytes)
	if err != nil {
		panic(err)
	}
	return canonical
}

func (d Deployment) Version() int64 {
	if _, ok := d["version"]; !ok {
		return -1
	}
	return cast.ToInt64(d["version"])
}

// ProcessBelongsToResource determines if `processName` belongs to `resourceName`.
func (d Deployment) ProcessBelongsToResource(processName, resourceName string) bool {
	if stringutil.Contains(d.GetProcessNames(ShardedCluster{}, resourceName), processName) {
		return true
	}
	if stringutil.Contains(d.GetProcessNames(ReplicaSet{}, resourceName), processName) {
		return true
	}
	if stringutil.Contains(d.GetProcessNames(Standalone{}, resourceName), processName) {
		return true
	}

	return false
}

// GetNumberOfExcessProcesses calculates how many processes do not belong to
// this resource.
func (d Deployment) GetNumberOfExcessProcesses(resourceName string) int {
	processNames := d.GetAllProcessNames()
	excessProcesses := len(processNames)
	for _, p := range processNames {
		if d.ProcessBelongsToResource(p, resourceName) {
			excessProcesses -= 1
		}
	}
	// Edge case: for sharded cluster it's ok to have junk replica sets during scale down - we consider them as
	// belonging to sharded cluster
	if d.getShardedClusterByName(resourceName) != nil {
		for _, r := range d.findReplicaSetsRemovedFromShardedCluster(resourceName) {
			excessProcesses -= len(d.GetProcessNames(ReplicaSet{}, r))
		}
	}

	return excessProcesses
}

func (d Deployment) SetRoles(roles []mdbv1.MongoDbRole) {
	d["roles"] = roles
}

func (d Deployment) GetRoles() []mdbv1.MongoDbRole {
	result := make([]mdbv1.MongoDbRole, 0)

	rolesBytes, err := json.Marshal(d["roles"])
	if err != nil {
		return nil
	}

	if err := json.Unmarshal(rolesBytes, &result); err != nil {
		return nil
	}

	return result
}

// GetAgentVersion returns the current version of all Agents in the deployment. It's empty until the
// 'automationConfig/updateAgentVersions' endpoint is called the first time
func (d Deployment) GetAgentVersion() string {
	agentVersionMap := util.ReadOrCreateMap(d, "agentVersion")
	return maputil.ReadMapValueAsString(agentVersionMap, "name")
}

func (d Deployment) Debug(l *zap.SugaredLogger) {
	dep := Deployment{}
	for key, value := range d {
		if key != "mongoDbVersions" {
			dep[key] = value
		}
	}
	b, err := json.MarshalIndent(dep, "", "  ")
	if err != nil {
		fmt.Println("error:", err)
	}
	l.Debugf(">> Deployment: \n %s \n", string(b))
}

// ProcessesCopy returns the COPY of processes in the deployment.
func (d Deployment) ProcessesCopy() []Process {
	return d.deepCopy().getProcesses()
}

// ReplicaSetsCopy returns the COPY of replicasets in the deployment.
func (d Deployment) ReplicaSetsCopy() []ReplicaSet {
	return d.deepCopy().getReplicaSets()
}

// ShardedClustersCopy returns the COPY of sharded clusters in the deployment.
func (d Deployment) ShardedClustersCopy() []ShardedCluster {
	return d.deepCopy().getShardedClusters()
}

// MonitoringVersionsCopy returns the COPY of monitoring versions in the deployment.
func (d Deployment) MonitoringVersionsCopy() []interface{} {
	return d.deepCopy().getMonitoringVersions()
}

// BackupVersionsCopy returns the COPY of backup versions in the deployment.
func (d Deployment) BackupVersionsCopy() []interface{} {
	return d.deepCopy().getBackupVersions()
}

// ***************************************** Private methods ***********************************************************

func (d Deployment) getReplicaSetProcessNames(name string) []string {
	processNames := make([]string, 0)
	if rs := d.getReplicaSetByName(name); rs != nil {
		for _, member := range rs.Members() {
			processNames = append(processNames, member.Name())
		}
	}
	return processNames
}

// GetShardedClusterShardProcessNames returns the process names for sharded cluster named "name" of index "shardNum".
func (d Deployment) GetShardedClusterShardProcessNames(name string, shardNum int) []string {
	if sc := d.getShardedClusterByName(name); sc != nil {
		if shardNum < 0 || shardNum >= len(sc.shards()) {
			return nil
		}
		return d.getReplicaSetProcessNames(sc.shards()[shardNum].rs())
	}
	return nil
}

// getShardedClusterShardsProcessNames returns the process names  fo sharded cluster named "name".
func (d Deployment) getShardedClusterShardsProcessNames(name string) []string {
	processNames := make([]string, 0)
	if sc := d.getShardedClusterByName(name); sc != nil {
		for i := range sc.shards() {
			processNames = append(processNames, d.GetShardedClusterShardProcessNames(name, i)...)
		}
	}
	return processNames
}

// GetShardedClusterConfigProcessNames returns the process names of config servers of the sharded cluster named "name"
func (d Deployment) GetShardedClusterConfigProcessNames(name string) []string {
	if sc := d.getShardedClusterByName(name); sc != nil {
		return d.getReplicaSetProcessNames(sc.ConfigServerRsName())
	}
	return nil
}

// GetShardedClusterMongosProcessNames returns the process names of mongos of the sharded cluster named "name"
func (d Deployment) GetShardedClusterMongosProcessNames(name string) []string {
	if sc := d.getShardedClusterByName(name); sc != nil {
		return d.getMongosProcessesNames(name)
	}
	return nil
}

func (d Deployment) getShardedClusterProcessNames(name string) []string {
	processNames := d.getShardedClusterShardsProcessNames(name)
	processNames = append(processNames, d.GetShardedClusterConfigProcessNames(name)...)
	processNames = append(processNames, d.GetShardedClusterMongosProcessNames(name)...)
	return processNames
}

func (d Deployment) mergeMongosProcesses(opts DeploymentShardedClusterMergeOptions, log *zap.SugaredLogger) error {
	// First removing old mongos processes
	for _, p := range d.getMongosProcessesNames(opts.Name) {
		found := false
		for _, v := range opts.MongosProcesses {
			if p == v.Name() {
				found = true
				break
			}
		}
		if !found {
			d.removeProcesses([]string{p}, log)
			log.Debugw("Removed redundant mongos process", "name", p)
		}
	}
	// Making sure changes to existing mongos processes are propagated to new ones
	if cntMongosProcesses := len(d.getMongosProcessesNames(opts.Name)); cntMongosProcesses > 0 && cntMongosProcesses < len(opts.MongosProcesses) {
		if err := d.copyFirstProcessToNewPositions(opts.MongosProcesses, cntMongosProcesses, log); err != nil {
			// I guess this error is not so serious to fail the whole process - mongoses will be scaled up anyway
			log.Error("Failed to copy first mongos process (so new mongos processes may miss Ops Manager changes done to "+
				"existing mongos processes): %w", err)
		}
	}

	// Then merging mongos processes with existing ones
	for _, p := range opts.MongosProcesses {
		if p.ProcessType() != ProcessTypeMongos {
			return xerrors.New(`all mongos processes must have processType="mongos"`)
		}
		p.setCluster(opts.Name)
		d.MergeStandalone(p, opts.MongosAdditionalOptionsDesired, opts.MongosAdditionalOptionsPrev, log)
	}
	return nil
}

func (d Deployment) getMongosProcessesNames(clusterName string) []string {
	processNames := make([]string, 0)
	for _, p := range d.getProcesses() {
		if p.ProcessType() == ProcessTypeMongos && p.cluster() == clusterName {
			processNames = append(processNames, p.Name())
		}
	}
	return processNames
}

func (d Deployment) mergeConfigReplicaSet(opts DeploymentShardedClusterMergeOptions, l *zap.SugaredLogger) {
	for _, p := range opts.ConfigServerRs.Processes {
		p.setClusterRoleConfigSrv()
	}

	d.MergeReplicaSet(opts.ConfigServerRs, opts.ConfigServerAdditionalOptionsDesired, opts.ConfigServerAdditionalOptionsPrev, l)
}

// mergeShards does merge of replicasets for shards (which in turn merge each process) and merge or add the sharded cluster
// element as well
func (d Deployment) mergeShards(opts DeploymentShardedClusterMergeOptions, log *zap.SugaredLogger) bool {
	// First merging the individual replica sets for each shard
	for _, v := range opts.Shards {
		d.MergeReplicaSet(v, opts.ShardAdditionalOptionsDesired, opts.ShardAdditionalOptionsPrev, log)
	}
	cluster := NewShardedCluster(opts.Name, opts.ConfigServerRs.Rs.Name(), opts.Shards)

	// Merging "sharding" json value
	for _, s := range d.getShardedClusters() {
		if s.Name() == opts.Name {
			s.mergeFrom(cluster)
			log.Debug("Merged sharded cluster into existing one")

			return d.handleShardsRemoval(opts.Finalizing, s, log)
		}
	}
	// Adding the new sharded cluster
	d.addShardedCluster(cluster)
	log.Debug("Added sharded cluster as current OM deployment didn't have it")
	return false
}

// handleShardsRemoval is a complicated method handling different scenarios.
// - 'draining' array is empty and no extra shards were found in OM which should be removed - return
// - if 'finalizing' == false - this means that this is the 1st phase of the process - when the shards are due to be removed
// or have already been removed and their replica sets are added/already sit in the 'draining' array. Note, that this
// method can be called many times while in the 1st phase and 'draining' array is not empty - this means that the agent
// is performing the shards rebalancing
// - if 'finalizing' == true - this means that this is the 2nd phase of the process - when the shards were removed
// from the sharded cluster and their data was rebalanced to the rest of the shards. Now we can remove the replica sets
// and their processes and clean the 'draining' array.
func (d Deployment) handleShardsRemoval(finalizing bool, s ShardedCluster, log *zap.SugaredLogger) bool {
	junkReplicaSets := d.findReplicaSetsRemovedFromShardedCluster(s.Name())

	if len(junkReplicaSets) == 0 {
		return false
	}

	if !finalizing {
		if len(junkReplicaSets) > 0 {
			s.addToDraining(junkReplicaSets)
		}
		log.Infof("The following shards are scheduled for removal: %s", s.draining())
		return true
	} else if len(junkReplicaSets) > 0 {
		// Cleaning replica sets which used to be shards in past iterations.
		s.removeDraining()
		d.removeReplicaSets(junkReplicaSets, log)
		log.Debugw("Removed replica sets as they were removed from sharded cluster", "replica sets", junkReplicaSets)
	}
	return false
}

// GetAllProcessNames returns a list of names of processes in this deployment. This is, the names of all processes
// in the `processes` attribute of the deployment object.
func (d Deployment) GetAllProcessNames() (names []string) {
	for _, p := range d.getProcesses() {
		names = append(names, p.Name())
	}
	return
}

func (d Deployment) getProcesses() []Process {
	if _, ok := d["processes"]; !ok {
		return []Process{}
	}
	switch v := d["processes"].(type) {
	case []Process:
		return v
	case []interface{}:
		// seems we cannot directly cast the array of interfaces to array of Processes - have to manually copy references
		ans := make([]Process, len(v))
		for i, val := range v {
			ans[i] = NewProcessFromInterface(val)
		}
		return ans
	default:
		panic(fmt.Sprintf("Unexpected type of processes variable: %T", v))
	}
}

func (d Deployment) getProcessesHostNames(names []string) []string {
	ans := make([]string, len(names))

	for i, n := range names {
		if p := d.getProcessByName(n); p != nil {
			ans[i] = p.HostName()
		}
	}
	return ans
}

func (d Deployment) setProcesses(processes []Process) {
	d["processes"] = processes
}

func (d Deployment) addProcess(p Process) {
	d.setProcesses(append(d.getProcesses(), p))
}

func (d Deployment) removeProcesses(processNames []string, log *zap.SugaredLogger) {
	// (CLOUDP-37709) implementation ideas: we remove agents for the processes if they are removed. Note, that
	// processes removal happens also during merge operations - so hypothetically if OM added some processes that were
	// removed by the Operator on merge - the agents will be removed from config as well. Seems this is quite safe and
	// in the Operator-managed environment we'll never get the situation when some agents reside on the hosts which are
	// not some processes.
	d.RemoveMonitoringAndBackup(processNames, log)

	processes := make([]Process, 0)

	for _, p := range d.getProcesses() {
		found := false
		for _, p2 := range processNames {
			if p.Name() == p2 {
				found = true
				break
			}
		}
		if !found {
			processes = append(processes, p)
		}
	}

	d.setProcesses(processes)
}

func (d Deployment) removeReplicaSets(replicaSets []string, log *zap.SugaredLogger) {
	for _, v := range replicaSets {
		_ = d.RemoveReplicaSetByName(v, log)
	}
}

func (d Deployment) getProcessByName(name string) *Process {
	for _, p := range d.getProcesses() {
		if p.Name() == name {
			return &p
		}
	}

	return nil
}

func (d Deployment) getReplicaSetByName(name string) *ReplicaSet {
	for _, r := range d.getReplicaSets() {
		if r.Name() == name {
			return &r
		}
	}

	return nil
}

func (d Deployment) getShardedClusterByName(name string) *ShardedCluster {
	for _, s := range d.getShardedClusters() {
		if s.Name() == name {
			return &s
		}
	}

	return nil
}

func (d Deployment) getReplicaSets() []ReplicaSet {
	switch v := d["replicaSets"].(type) {
	case []ReplicaSet:
		return v
	case []interface{}:
		ans := make([]ReplicaSet, len(v))
		for i, val := range v {
			ans[i] = NewReplicaSetFromInterface(val)
		}
		return ans
	default:
		panic(fmt.Sprintf("Unexpected type of replicasets variable: %T", v))
	}
}

func (d Deployment) setReplicaSets(replicaSets []ReplicaSet) {
	d["replicaSets"] = replicaSets
}

func (d Deployment) addReplicaSet(rs ReplicaSet) {
	d.setReplicaSets(append(d.getReplicaSets(), rs))
}

func (d Deployment) getShardedClusters() []ShardedCluster {
	switch v := d["sharding"].(type) {
	case []ShardedCluster:
		return v
	case []interface{}:
		ans := make([]ShardedCluster, len(v))
		for i, val := range v {
			ans[i] = NewShardedClusterFromInterface(val)
		}
		return ans
	default:
		panic(fmt.Sprintf("Unexpected type of sharding variable: %T", v))
	}
}

func (d Deployment) setShardedClusters(shardedClusters []ShardedCluster) {
	d["sharding"] = shardedClusters
}

func (d Deployment) addShardedCluster(shardedCluster ShardedCluster) {
	d.setShardedClusters(append(d.getShardedClusters(), shardedCluster))
}

func (d Deployment) getMonitoringVersions() []interface{} {
	return d["monitoringVersions"].([]interface{})
}

func (d Deployment) getBackupVersions() []interface{} {
	return d["backupVersions"].([]interface{})
}

func (d Deployment) setMonitoringVersions(monitoring []interface{}) {
	d["monitoringVersions"] = monitoring
}

func (d Deployment) setBackupVersions(backup []interface{}) {
	d["backupVersions"] = backup
}

func (d Deployment) getTLS() map[string]interface{} {
	return util.ReadOrCreateMap(d, "tls")
}

// findReplicaSetsRemovedFromShardedCluster finds all replica sets which look like shards that have been removed from
// the sharded cluster.
// To make this method work correctly the shards MUST have the same prefix as a shard (which is true for the
// Operator-created resource)
func (d Deployment) findReplicaSetsRemovedFromShardedCluster(clusterName string) []string {
	shardedCluster := d.getShardedClusterByName(clusterName)
	clusterReplicaSets := shardedCluster.getAllReplicaSets()
	var ans []string

	for _, v := range d.getReplicaSets() {
		if !stringutil.Contains(clusterReplicaSets, v.Name()) && isShardOfShardedCluster(clusterName, v.Name()) {
			ans = append(ans, v.Name())
		}
	}
	return ans
}

func isShardOfShardedCluster(clusterName, rsName string) bool {
	return regexp.MustCompile(`^` + clusterName + `-[0-9]+$`).MatchString(rsName)
}

// removeMonitoring removes the monitoring agent configuration that match any of processes hosts 'processNames' parameter
// Note, that by contract there will be only one monitoring agent, but the method tries to be maximum safe and clean
// all matches (maybe someone "hacked" the automation config manually and added the monitoring agents there)
// Note 2: it's ok if nothing was removed as the processes in the array may be from replica set from sharded cluster
// which doesn't have a monitoring agents (one monitoring agent per cluster)
func (d Deployment) removeMonitoring(processNames []string) {
	monitoringVersions := d.getMonitoringVersions()
	updatedMonitoringVersions := make([]interface{}, 0)
	hostNames := d.getProcessesHostNames(processNames)
	for _, m := range monitoringVersions {
		monitoring := m.(map[string]interface{})
		hostname := monitoring["hostname"].(string)
		if !stringutil.Contains(hostNames, hostname) {
			updatedMonitoringVersions = append(updatedMonitoringVersions, m)
		} else {
			hostNames = stringutil.Remove(hostNames, hostname)
		}
	}

	d.setMonitoringVersions(updatedMonitoringVersions)
}

// addBackup adds backup agent configuration for each of the processes of deployment
func (d Deployment) addBackup(log *zap.SugaredLogger) {
	backupVersions := d.getBackupVersions()
	for _, p := range d.getProcesses() {
		found := false
		var backupVersion map[string]interface{}
		for _, b := range backupVersions {
			backupVersion = b.(map[string]interface{})
			if backupVersion["hostname"] == p.HostName() {
				found = true
				break
			}
		}

		if !found {
			backupVersion = map[string]interface{}{"hostname": p.HostName(), "name": BackupAgentDefaultVersion}
			backupVersions = append(backupVersions, backupVersion)
			log.Debugw("Added backup agent configuration", "host", p.HostName())
		}
	}
	d.setBackupVersions(backupVersions)
}

// removeBackup removes the backup versions from Deployment that are in 'hosts' array parameter
func (d Deployment) removeBackup(processNames []string, log *zap.SugaredLogger) {
	backupVersions := d.getBackupVersions()
	updatedBackupVersions := make([]interface{}, 0)
	initialLength := len(processNames)
	hostNames := d.getProcessesHostNames(processNames)
	for _, b := range backupVersions {
		backup := b.(map[string]interface{})
		hostname := backup["hostname"].(string)
		if !stringutil.Contains(hostNames, hostname) {
			updatedBackupVersions = append(updatedBackupVersions, b)
		} else {
			hostNames = stringutil.Remove(hostNames, hostname)
		}
	}

	if len(hostNames) != 0 {
		// Note, that we don't error/warn here as there can be plenty of reasons why the config is not here (e.g. some
		// process added to OM deployment manually that doesn't have corresponding backup config). Warn prints the
		// stacktrace which looks quite scary
		log.Infof("The following hosts were not removed from backup config as they were not found: %s", hostNames)
	} else {
		log.Debugf("Removed backup agent configuration for %d host(s)", initialLength)
	}
	d.setBackupVersions(updatedBackupVersions)
}

// copyFirstProcessToNewPositions is used when scaling up replica set / set of mongos processes. Its main goal is to clone
// the sample existing deployment process as many times as the number of new processes to be added. The copies get
// the names of the "new" processes so that the following "mergeStandalone" operation could merge "Operator owned" information
// back into copies. This allows to keep all changes made by OM to existing processes and overwrite only the fields that
// Operator is responsible for.
// So if current RS deployment that came from OM has processes A, B, C and operator wants to scale up on 2 more members
// (meaning it wants to add X, Y processes) - then in the end of this function deployment will contain processes A, B, C,
// and X, Y where X, Y will be complete copies of A instead of names and aliases.
// "processes" is the array of "Operator view" processes (so for the example above they will be "A, B, C, X, Y")
// "idxOfFirstNewMember" is the index of the first NEW member. So for the example above it will be 3
func (d Deployment) copyFirstProcessToNewPositions(processes []Process, idxOfFirstNewMember int, log *zap.SugaredLogger) error {
	newProcesses := processes[idxOfFirstNewMember:]

	var sampleProcess Process

	// The sample process must be the one that exist in OM deployment - so if for some reason OM added some
	// processes to Deployment (and they won't get into merged deployment) - we must find the first one matching
	// As an example let's consider the RS that contained processes A, B, C and then OM UI removed the processes A and B
	// So the "processes" array (which is Kubernetes view of RS) will still contain A, B, C, … so in the end the sample
	// process will be C (as this is the only process that intersects in the "OM view" and "Kubernetes view" and it will
	// get into final deployment
	for _, v := range processes {
		if d.getProcessByName(v.Name()) != nil {
			sampleProcess = *d.getProcessByName(v.Name())
			break
		}
	}
	// If sample process has not been found - that means that all processes in OM deployment are some fake - we'll remove
	// them anyway and there is no need in merging
	// Example: OM UI removed A, B, C and added P, T, R, but Kubernetes will still try to create the RS of A, B, C - and
	// will remove faked processes in the end. So no OM "sample" would exist in this case as all processes will be brand
	// new
	if sampleProcess == nil {
		return nil
	}

	for _, p := range newProcesses {
		sampleProcessCopy, err := sampleProcess.DeepCopy()
		if err != nil {
			return xerrors.Errorf("failed to make a copy of Process %s: %w", sampleProcess.Name(), err)
		}
		sampleProcessCopy.setName(p.Name())

		// add here other attributes that mustn't be copied (maybe some others should be added here)
		delete(sampleProcessCopy, "alias")

		// This is just fool protection - if for some reason the process already exists in deployment (it mustn't actually)
		// then we don't add the copy of sample one
		if d.getProcessByName(p.Name()) == nil {
			d.addProcess(sampleProcessCopy)
			log.Debugw("Added the copy of the process to the end of deployment processes", "process name",
				sampleProcess.Name(), "new process name", sampleProcessCopy.Name())
		}
	}
	return nil
}

func (d Deployment) processesHaveInternalClusterAuthentication(processes []Process) bool {
	for _, p := range processes {
		if p.HasInternalClusterAuthentication() {
			return true
		}
	}
	return false
}

// limitVotingMembers ensures the number of voting members in the replica set is not more than 7 members
func (d Deployment) limitVotingMembers(rsName string) {
	r := d.getReplicaSetByName(rsName)

	numberOfVotingMembers := 0
	for _, v := range r.Members() {
		if v.Votes() > 0 {
			numberOfVotingMembers++
		}
		if numberOfVotingMembers > 7 {
			v.setVotes(0).setPriority(0)
		}
	}
}

func (d Deployment) deepCopy() Deployment {
	var depCopy Deployment

	depCopy, err := util.MapDeepCopy(d)
	if err != nil {
		panic(err)
	}
	return depCopy
}
