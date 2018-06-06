package om

import (
	"encoding/json"
	"errors"
	"fmt"

	"encoding/gob"

	"go.uber.org/zap"
)

const (
	// Note that these two constants shouldn't be changed often as AutomationAgent upgrades both other agents automatically
	MonitoringAgentDefaultVersion = "6.4.0.433-1"
	BackupAgentDefaultVersion     = "6.6.0.959-1"
)

func init() {
	gob.Register(map[string]interface{}{})
	gob.Register(map[string]int{})
	gob.Register(map[string]string{})
	gob.Register(ProcessTypeMongos)
}

type Deployment map[string]interface{}

func BuildDeploymentFromBytes(jsonBytes []byte) (ans Deployment, err error) {
	cc := Deployment{}
	if err := json.Unmarshal(jsonBytes, &cc); err != nil {
		return nil, err
	}
	return cc, nil
}

func NewDeployment() Deployment {
	ans := Deployment{}
	ans.setProcesses(make([]Process, 0))
	ans.setReplicaSets(make([]ReplicaSet, 0))
	ans.setShardedClusters(make([]ShardedCluster, 0))
	ans.setMonitoringVersions(make([]interface{}, 0))
	ans.setBackupVersions(make([]interface{}, 0))
	return ans
}

// merge Standalone. If we found the process with the same name - update some fields there. Otherwise add the new one
func (d Deployment) MergeStandalone(standaloneMongo Process, l *zap.SugaredLogger) {
	if l == nil {
		l = zap.S()
	}
	log := l.With("standalone", standaloneMongo)

	// merging process in case exists, otherwise adding it
	for _, pr := range d.getProcesses() {
		if pr.Name() == standaloneMongo.Name() {
			pr.mergeFrom(standaloneMongo)
			log.Debug("Merged process into existing one")
			return
		}
	}
	d.addProcess(standaloneMongo)
	log.Debug("Added process as current OM deployment didn't have it")
}

// MergeReplicaSet merges the replica set and its members to the deployment. If "alien" RS members are removed after merge -
// corresponding processes are removed as well.
// So far we don't configure anything for RS except it's name (though the API supports many other parameters
// and we may change this in future)
func (d Deployment) MergeReplicaSet(replicaSet ReplicaSetWithProcesses, l *zap.SugaredLogger) {
	if l == nil {
		l = zap.S()
	}
	log := l.With("replicaSet", replicaSet.Rs.Name())

	r := d.getReplicaSetByName(replicaSet.Rs.Name())

	// If the new replica set is bigger than old one - we need to copy first member to positions of new members so that
	// they were merged with operator replica sets on next step
	// (in case OM made any changes to existing processes - these changes must be propagated to new members).
	if r != nil && len(replicaSet.Rs.members()) > len(r.members()) {
		if err := d.copyFirstProcessToNewPositions(replicaSet.Processes, len(r.members()), l); err != nil {
			// I guess this error is not so serious to fail the whole process - RS will be scaled up anyway
			log.Error("Failed to copy first process (so new replica set processes may miss Ops Manager changes done to "+
				"existing replica set processes): %s", err)
		}
	}

	// Merging all RS processes
	for _, p := range replicaSet.Processes {
		d.MergeStandalone(p, log)
	}

	if r == nil {
		// Adding a new Replicaset
		d.addReplicaSet(replicaSet.Rs)
		log.Debugw("Added replica set as current OM deployment didn't have it")
	} else {

		processesToRemove := r.mergeFrom(replicaSet.Rs)
		log.Debugw("Merged replica set into existing one")

		if len(processesToRemove) > 0 {
			d.removeProcesses(processesToRemove)
			log.Debugw("Removed processes as they were removed from replica set", "processesToRemove", processesToRemove)
		}
	}
}

func (d Deployment) MergeShardedCluster(name string, mongosProcesses []Process, configServerRs ReplicaSetWithProcesses,
	shards []ReplicaSetWithProcesses) error {
	log := zap.S().With("sharded cluster", name)

	err := d.mergeMongosProcesses(name, mongosProcesses, log)
	if err != nil {
		return err
	}

	d.mergeConfigReplicaSet(configServerRs, log)

	d.mergeShards(name, configServerRs, shards, log)

	return nil
}

// AddMonitoringAndBackup adds only one monitoring agent on the specified hostname if it isn't configured yet.
// The logic for choosing the right host name is as following: each resources (standalone, RS, SC) must choose the consistent
// process and use its hostname to install monitoring agent. So each resource in OM Deployment will have a single monitoring
// agent installed
// Also the backup agent is added to each server
// Note, that these two are deliberately combined together as all clients (standalone, rs etc) need both backup and monitoring
// together
func (d Deployment) AddMonitoringAndBackup(hostName string, log *zap.SugaredLogger) {

	if len(d.getProcesses()) == 0 {
		return
	}

	d.addMonitoring(hostName, log)
	d.addBackup(log)
}

func (d Deployment) DisableProcesses(processNames []string) {
	for _, p := range processNames {
		d.getProcessByName(p).SetDisabled(true)
	}
}

func (d Deployment) MarkRsMembersUnvoted(rsName string, rsMembers []string) {
	for _, m := range rsMembers {
		d.getReplicaSetByName(rsName).findMemberByName(m).setVotes(0).setPriority(0)
	}
}

func (d Deployment) RemoveProcessByName(name string) error {
	s := d.getProcessByName(name)
	if s == nil {
		return errors.New(fmt.Sprintf("Standalone %s does not exist", name))
	}

	d.removeProcesses([]string{s.Name()})

	return nil
}

func (d Deployment) RemoveReplicaSetByName(name string) error {
	rs := d.getReplicaSetByName(name)
	if rs == nil {
		return errors.New("ReplicaSet does not exist")
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

	members := rs.members()
	processNames := make([]string, len(members))
	for _, el := range members {
		processNames = append(processNames, el.Name())
	}
	d.removeProcesses(processNames)

	return nil
}

func (d Deployment) RemoveShardedClusterByName(clusterName string) error {
	sc := d.getShardedClusterByName(clusterName)
	if sc == nil {
		return errors.New("Sharded Cluster does not exist")
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
	d.removeReplicaSets(shardNames)

	// 3. Remove config server replicaset
	d.RemoveReplicaSetByName(sc.ConfigServerRsName())

	// 4. Remove mongos processes for cluster
	d.removeProcesses(d.getMongosProcessesNames(clusterName))

	return nil
}

func (d Deployment) Debug() {
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		fmt.Println("error:", err)
	}
	fmt.Print(string(b))
}

// ***************************************** Private methods ***********************************************************

func (d Deployment) mergeMongosProcesses(clusterName string, mongosProcesses []Process, log *zap.SugaredLogger) error {
	// First removing old mongos processes
	for _, p := range d.getMongosProcessesNames(clusterName) {
		found := false
		for _, v := range mongosProcesses {
			if p == v.Name() {
				found = true
				break
			}
		}
		if !found {
			d.removeProcesses([]string{p})
			log.Debugw("Removed redundant mongos process", "name", p)
		}
	}
	// Making sure changes to existing mongos processes are propagated to new ones
	if cntMongosProcesses := len(d.getMongosProcessesNames(clusterName)); cntMongosProcesses > 0 && cntMongosProcesses < len(mongosProcesses) {
		if err := d.copyFirstProcessToNewPositions(mongosProcesses, cntMongosProcesses, log); err != nil {
			// I guess this error is not so serious to fail the whole process - mongoses will be scaled up anyway
			log.Error("Failed to copy first mongos process (so new mongos processes may miss Ops Manager changes done to "+
				"existing mongos processes): %s", err)
		}
	}

	// Then merging mongos processes with existing ones
	for _, p := range mongosProcesses {
		if p.ProcessType() != ProcessTypeMongos {
			return errors.New("All mongos processes must have processType=\"mongos\"!")
		}
		p.setCluster(clusterName)
		d.MergeStandalone(p, log)
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

func (d Deployment) mergeConfigReplicaSet(replicaSet ReplicaSetWithProcesses, l *zap.SugaredLogger) {
	for _, p := range replicaSet.Processes {
		p.setClusterRoleConfigSrv()
	}

	d.MergeReplicaSet(replicaSet, l)
}

func (d Deployment) mergeShards(clusterName string, configServerRs ReplicaSetWithProcesses,
	shards []ReplicaSetWithProcesses, log *zap.SugaredLogger) {
	// First merging the individual replica sets for each shard
	for _, v := range shards {
		d.MergeReplicaSet(v, log)
	}
	cluster := NewShardedCluster(clusterName, configServerRs.Rs.Name(), shards)

	// Merging "sharding" json value
	for _, s := range d.getShardedClusters() {
		if s.Name() == clusterName {
			replicaSetToRemove := s.mergeFrom(cluster)
			log.Debug("Merged sharded cluster into existing one")

			if len(replicaSetToRemove) > 0 {
				d.removeReplicaSets(replicaSetToRemove)
				log.Debugw("Removed replica sets as they were removed from sharded cluster", "replica sets", replicaSetToRemove)
			}
			return
		}
	}
	// Adding the new sharded cluster
	d.addShardedCluster(cluster)
	log.Debug("Added sharded cluster as current OM deployment didn't have it")
}

func (d Deployment) getProcesses() []Process {
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

func (d Deployment) setProcesses(processes []Process) {
	d["processes"] = processes
}

func (d Deployment) addProcess(p Process) {
	d.setProcesses(append(d.getProcesses(), p))
}

func (d Deployment) removeProcesses(processNames []string) {
	processes := make([]Process, 0)

	for _, p := range d.getProcesses() {
		found := false
		for _, p2 := range processNames {
			if p.Name() == p2 {
				found = true
			}
		}
		if !found {
			processes = append(processes, p)
		}
	}

	d.setProcesses(processes)
}

func (d Deployment) removeReplicaSets(replicaSets []string) {
	for _, v := range replicaSets {
		d.RemoveReplicaSetByName(v)
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

func (d Deployment) setMonitoringVersions(monitoring []interface{}) {
	d["monitoringVersions"] = monitoring
}

func (d Deployment) setBackupVersions(monitoring []interface{}) {
	d["backupVersions"] = monitoring
}

// addMonitoring adds one single monitoring agent for specified host name.
// Note that automation agent will update the monitoring agent to the latest version automatically
func (d Deployment) addMonitoring(hostName string, log *zap.SugaredLogger) {
	monitoringVersions := d["monitoringVersions"].([]interface{})
	found := false
	for _, b := range monitoringVersions {
		monitoring := b.(map[string]interface{})
		if monitoring["hostname"] == hostName {
			found = true
			break
		}
	}
	if !found {
		monitoringVersions = append(monitoringVersions,
			map[string]interface{}{"hostname": hostName, "name": MonitoringAgentDefaultVersion})
		d.setMonitoringVersions(monitoringVersions)

		log.Debugw("Added monitoring agent configuration", "host", hostName)
	}
}

// addBackup adds backup agent configuration for each of the processes of deployment
func (d Deployment) addBackup(log *zap.SugaredLogger) {
	backupVersions := d["backupVersions"].([]interface{})
	for _, p := range d.getProcesses() {
		found := false
		for _, b := range backupVersions {
			backup := b.(map[string]interface{})
			if backup["hostname"] == p.HostName() {
				found = true
				break
			}
		}
		if !found {
			backupVersions = append(backupVersions,
				map[string]interface{}{"hostname": p.HostName(), "name": BackupAgentDefaultVersion})
			d.setBackupVersions(backupVersions)

			log.Debugw("Added backup agent configuration", "host", p.HostName())
		}
	}
}

// copyFirstProcessToNewPositions is used when scaling up replica set / set of mongos processes. Its main goal is to clone
// the sample existing deployment process as many times as the number of new processes to be added. The copies get
// the names of the "new" processes so that the following "mergeStandalone" operation could merge "Operator owned" information
// back into copies. This allows to keep all changes made by OM to existing processes and overwrite only the fields that
// Operator is responsible for.
// So if current RS deployment that came from OM has processes A, B, C and operator wants to scale up on 2 more members
// (meaning it wants to add X, Y processes) - then in the end of this function deployment will contain processes A, B, C,
// and X, Y where X, Y will be complete copies of A instead of names and aliases.
// "processes" is the array of "Operator view" processes (so for the example above they will be "A, B, C, X, Y"
// "idxOfFirstNewMember" is the index of the first NEW member. So for the example above it will be 3
func (d Deployment) copyFirstProcessToNewPositions(processes []Process, idxOfFirstNewMember int, log *zap.SugaredLogger) error {
	newProcesses := processes[idxOfFirstNewMember:]

	var sampleProcess Process

	// The sample process must be the one that exist in OM deployment - so if for some reason OM added some
	// processes to Deployment (and they won't get into merged deployment) - we must find the first one matching
	// As an example let's consider the RS that contained processes A, B, C and then OM UI removed the processes A and B
	// So the "processes" array (which is Kubernetes view of RS) will still contain A, B, C, .. so in the end the sample
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
			return errors.New(fmt.Sprintf("Failed to make a copy of Process %s: %s", sampleProcess.Name(), err))
		}
		sampleProcessCopy.setName(p.Name())

		// add here other attributes that mustn's be copied (may be some others should be added here)
		delete(sampleProcessCopy, "alias")

		// This is just fool protection - if for some reasons the process already exists in deployment (it mustn't actually)
		// then we don't add the copy of sample one
		if d.getProcessByName(p.Name()) == nil {
			d.addProcess(sampleProcessCopy)
			log.Debugw("Added the copy of the process to the end of deployment processes", "process name",
				sampleProcess.Name(), "new process name", sampleProcessCopy.Name())
		}
	}
	return nil
}
