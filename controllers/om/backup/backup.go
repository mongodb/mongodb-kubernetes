package backup

import (
	"errors"
	"fmt"

	"go.uber.org/zap"
	"golang.org/x/xerrors"

	"github.com/10gen/ops-manager-kubernetes/controllers/om/apierror"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/env"
)

type MongoDbResourceType string

const (
	ReplicaSetType     MongoDbResourceType = "ReplicaSet"
	ShardedClusterType MongoDbResourceType = "ShardedCluster"
)

/*
for sharded cluster:

	{
	  "clusterName": "shannon",
	  "groupId": "5ba0c398a957713d7f8653bd",
	  "id": "5ba3d344a957713d7f8f43fd",
	  "lastHeartbeat": "2018-09-20T17:12:28Z",
	  "links": [ ... ],
	  "shardName": "shannon-0",
	  "typeName": "SHARDED_REPLICA_SET"
	}

for sharded cluster member:

	{
	  "clusterName": "shannon",
	  "groupId": "5ba0c398a957713d7f8653bd",
	  "id": "5ba4ec37a957713d7f9bcba0",
	  "lastHeartbeat": "2018-09-24T12:41:05Z",
	  "links": [ ... ],
	  "replicaSetName": "shannon-0",
	  "shardName": "shannon-0",
	  "typeName": "REPLICA_SET"
	}

for replica set:

	{
	  "clusterName": "liffey",
	  "groupId": "5ba0c398a957713d7f8653bd",
	  "id": "5ba8db64a957713d7fa5018b",
	  "lastHeartbeat": "2018-09-24T12:41:08Z",
	  "links": [ ... ],
	  "replicaSetName": "liffey",
	  "typeName": "REPLICA_SET"
	}
*/
type HostCluster struct {
	ReplicaSetName string `json:"replicaSetName"`
	ClusterName    string `json:"clusterName"`
	ShardName      string `json:"shardName"`
	TypeName       string `json:"typeName"`
}

type HostClusterReader interface {
	ReadHostCluster(clusterID string) (*HostCluster, error)
}

// StopBackupIfEnabled tries to find backup configuration for specified resource (can be Replica Set or Sharded Cluster -
// Ops Manager doesn't backup Standalones) and disable it.
func StopBackupIfEnabled(readUpdater ConfigHostReadUpdater, hostClusterReader HostClusterReader, name string, resourceType MongoDbResourceType, log *zap.SugaredLogger) error {
	response, err := readUpdater.ReadBackupConfigs()
	if err != nil {
		// If the operator can't read BackupConfigs, it might indicate that the Pods were removed before establishing
		// or activating monitoring for the deployment. But if this is a deletion process of the MDB resource, it needs
		// to be removed anyway, so we are logging the Error and continuing.
		// TODO: Discussion. To avoid removing dependant objects in a DELETE operation, a finalizer should be implemented
		// This finalizer would be required to add a "delay" to the deletion of the StatefulSet waiting for monitoring
		// to be activated at the project.
		var v *apierror.Error
		if errors.As(err, &v) {
			if v.ErrorCode == "CANNOT_GET_BACKUP_CONFIG_INVALID_STATE" {
				log.Warnf("Could not read backup configs for this deployment. Will continue with the removal of the objects. %s", err)
				return nil
			}
		}
		return err
	}

	for _, config := range response.Configs {
		l := log.With("cluster id", config.ClusterId)

		l.Debugw("Found backup/host config", "status", config.Status)
		// Any status except for inactive will result in API rejecting the deletion of resource - we need to disable backup
		if config.Status != Inactive {
			cluster, err := hostClusterReader.ReadHostCluster(config.ClusterId)
			if err != nil {
				l.Errorf("Failed to read information about HostCluster: %s", err)
			} else {
				l.Debugw("Read cluster information", "details", cluster)
			}
			// We need to compare the returned backup type with the given type because for sharded_clusters we
			// have 4 configs.
			// Three replica_sets and one sharded_replica_set.
			// We only want to disable the backup for the sharded_replica_set
			if cluster.ClusterName == name &&
				(resourceType == ReplicaSetType && cluster.TypeName == "REPLICA_SET" ||
					resourceType == ShardedClusterType && cluster.TypeName == "SHARDED_REPLICA_SET") {
				err = disableBackup(readUpdater, config, l)
				if err != nil {
					return err
				}
				l.Infow("Disabled backup for host cluster in Ops Manager", "host cluster name", cluster.ClusterName)
			}
		}
	}
	return nil
}

func disableBackup(readUpdater ConfigHostReadUpdater, backupConfig *Config, log *zap.SugaredLogger) error {
	if backupConfig.Status == Started {
		err := readUpdater.UpdateBackupStatus(backupConfig.ClusterId, Stopped)
		if err != nil {
			log.Errorf("Failed to stop backup for host cluster: %s", err)
		} else {
			if ok, msg := waitUntilBackupReachesStatus(readUpdater, backupConfig, Stopped, log); ok {
				log.Debugw("Stopped backup for host cluster")
			} else {
				log.Warnf("Failed to stop backup for host cluster in Ops Manager (timeout exhausted): %s", msg)
			}
		}
	}
	// We try to terminate in any case (it will fail if the backup config is not stopped)
	err := readUpdater.UpdateBackupStatus(backupConfig.ClusterId, Terminating)
	if err != nil {
		return err
	}
	if ok, msg := waitUntilBackupReachesStatus(readUpdater, backupConfig, Inactive, log); !ok {
		return xerrors.Errorf("Failed to disable backup for host cluster in Ops Manager (timeout exhausted): %s", msg)
	}
	return nil
}

func waitUntilBackupReachesStatus(configReader ConfigReader, backupConfig *Config, status Status, log *zap.SugaredLogger) (bool, string) {
	waitSeconds := env.ReadIntOrPanic(util.BackupDisableWaitSecondsEnv)
	retries := env.ReadIntOrPanic(util.BackupDisableWaitRetriesEnv)

	backupStatusFunc := func() (string, bool) {
		config, err := configReader.ReadBackupConfig(backupConfig.ClusterId)
		if err != nil {
			return fmt.Sprintf("Unable to read from OM API: %s", err), false
		}

		if config.Status == status {
			return "", true
		}
		return fmt.Sprintf("Current status: %s", config.Status), false
	}

	return util.DoAndRetry(backupStatusFunc, log, retries, waitSeconds)
}
