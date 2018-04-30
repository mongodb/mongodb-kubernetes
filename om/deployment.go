package om

import (
	"encoding/json"
	"errors"
	"fmt"

	"go.uber.org/zap"
)

type Deployment map[string]interface{}

func BuildDeploymentFromBytes(jsonBytes []byte) (ans *Deployment, err error) {
	cc := &Deployment{}
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
	return ans
}

// merge Standalone. If we found the process with the same name - update some fields there. Otherwise add the new one
func (d Deployment) MergeStandalone(standaloneMongo Process) {
	log := zap.S().With("process", standaloneMongo)

	// merging process in case exists, otherwise adding it
	for _, pr := range d.getProcesses() {
		if pr.Name() == standaloneMongo.Name() {
			pr.mergeFrom(standaloneMongo)
			log.Debug("Merged process into existing one")
			return
		}
	}
	d.setProcesses(append(d.getProcesses(), standaloneMongo))
	log.Debug("Added process as current OM deployment didn't have it")
}

// MergeReplicaSet merges the replica set and its members to the deployment. If "alien" RS members are removed after merge -
// corresponding processes are removed as well.
// So far we don't configure anything for RS except it's name (though the API supports many other parameters
// and we may change this in future)
func (d Deployment) MergeReplicaSet(replicaSet ReplicaSetWithProcesses) {
	log := zap.S().With("replicaSet", replicaSet.rs.Name())
	for _, p := range replicaSet.processes {
		d.MergeStandalone(p)
	}

	r := d.getReplicaSetByName(replicaSet.rs.Name())
	if r == nil {
		// Adding a new Replicaset
		d.setReplicaSets(append(d.getReplicaSets(), replicaSet.rs))
		log.Debugw("Added replica set as current OM deployment didn't have it")
	} else {
		processesToRemove := r.mergeFrom(replicaSet.rs)
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

	err := d.mergeMongosProcesses(name, mongosProcesses)
	if err != nil {
		return err
	}

	d.MergeReplicaSet(configServerRs)

	d.mergeShards(name, configServerRs, shards, log)

	return nil
}

// AddMonitoringAndBackup adds only one monitoring agent on the same host as the first process in the list if no monitoring
// agents are configured. Must be called after processes are added
// Also the backup agent is added to each server
// Note, that these two are deliberately combined together as all clients (standalone, rs etc) need both backup and monitoring
// together
func (d Deployment) AddMonitoringAndBackup() {

	if len(d.getProcesses()) == 0 {
		return
	}

	d.addMonitoring()
	d.addBackup()
}

func (d Deployment) DisableProcess(name string) {
	for _, el := range d.getProcesses() {
		if el.Name() == name {
			el.SetDisabled(true)
		}
	}
}

func (d Deployment) MarkRsMemberUnvoted(rsName, rsMemberName string) {
	d.getReplicaSetByName(rsName).findMemberByName(rsMemberName).setVotes(0).setPriority(0)
}

func (d Deployment) RemoveProcessByName(name string) error {
	s := d.getProcessByName(name)
	if s == nil {
		return errors.New(fmt.Sprintf("Standalone %s does not exist", name))
	}

	d.removeProcesses([]string{s.Name()})

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

func (d Deployment) mergeMongosProcesses(clusterName string, mongosProcesses []Process) error {
	// First removing old mongos processes
	for _, p := range d.getProcesses() {
		if p.ProcessType() == ProcessTypeMongos && p.Cluster() == clusterName {
			found := false
			for _, v := range mongosProcesses {
				if p.Name() == v.Name() {
					found = true
					break
				}
			}
			if !found {
				d.removeProcesses([]string{p.Name()})
			}
		}
	}
	// Then merging mongos processes with existing ones
	for _, p := range mongosProcesses {
		if p.ProcessType() != ProcessTypeMongos {
			return errors.New("All mongos processes must have processType=\"mongos\"!")
		}
		p.setCluster(clusterName)
		d.MergeStandalone(p)
	}
	return nil
}

func (d Deployment) mergeShards(clusterName string, configServerRs ReplicaSetWithProcesses,
	shards []ReplicaSetWithProcesses, log *zap.SugaredLogger) {
	// First merging the individual replica sets for each shard
	for _, v := range shards {
		d.MergeReplicaSet(v)
	}
	cluster := NewShardedCluster(clusterName, configServerRs.rs.Name(), shards)

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

// addMonitoring adds one single monitoring agent. Note that automation agent will update the monitoring agent to the
// latest version
func (d Deployment) addMonitoring() {
	monitoringVersions := d["monitoringVersions"].([]interface{})
	if len(monitoringVersions) == 0 {
		monitoringVersions = append(monitoringVersions,
			map[string]string{"hostname": d.getProcesses()[0].HostName(), "name": "6.1.2.402-1"})
		d["monitoringVersions"] = monitoringVersions

		zap.S().Debugw("Added monitoring agent configuration", "host", d.getProcesses()[0].HostName())
	}
}

// addBackup adds backup agent configuration for each of the processes of deployment
func (d Deployment) addBackup() {
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
				map[string]interface{}{"hostname": p.HostName(), "name": "6.6.1.965"})
			d["backupVersions"] = backupVersions

			zap.S().Debugw("Added backup agent configuration", "host", p.HostName())
		}
	}
}
