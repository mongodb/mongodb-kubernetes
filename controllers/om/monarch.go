package om

import (
	"fmt"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
)

// MaintainedMonarchComponents is the automation config section that tells the agent
// about the Monarch injector configuration for a standby cluster.
type MaintainedMonarchComponents struct {
	ReplicaSetID       string         `json:"replicaSetId"`
	ClusterPrefix      string         `json:"clusterPrefix"`
	InitialMode        string         `json:"initialMode"`
	AWSBucketName      string         `json:"awsBucketName"`
	AWSRegion          string         `json:"awsRegion"`
	AWSAccessKeyID     string         `json:"awsAccessKeyId"`
	AWSSecretAccessKey string         `json:"awsSecretAccessKey"`
	S3BucketEndpoint   string         `json:"s3BucketEndPoint,omitempty"`
	S3PathStyleAccess  bool           `json:"s3PathStyleAccess,omitempty"`
	InjectorConfig     InjectorConfig `json:"injectorConfig"`
}

type InjectorConfig struct {
	Version string          `json:"version"`
	SrcURI  string          `json:"srcURI,omitempty"`
	Shards  []InjectorShard `json:"shards"`
}

type InjectorShard struct {
	ShardID     string             `json:"shardId"`
	ReplSetName string             `json:"replSetName"`
	Instances   []InjectorInstance `json:"instances"`
}

type InjectorInstance struct {
	ID                 int    `json:"id"`
	Hostname           string `json:"hostname"`
	Disabled           bool   `json:"disabled"`
	Port               int    `json:"port"`
	ExternallyManaged  bool   `json:"externallyManaged"`
	HealthAPIEndpoint  string `json:"healthApiEndpoint"`
	MonarchAPIEndpoint string `json:"monarchApiEndpoint"`
}

// SetMaintainedMonarchComponents sets the maintainedMonarchComponents field in the automation config.
func (d Deployment) SetMaintainedMonarchComponents(mc []MaintainedMonarchComponents) {
	d["maintainedMonarchComponents"] = mc
}

// BuildMaintainedMonarchComponents builds the automation config entries for Monarch.
// For standby (injector) clusters, it creates InjectorInstance entries using Service DNS names.
// For active (shipper) clusters, it creates entries without injector instances.
func BuildMaintainedMonarchComponents(mdb *mdbv1.MongoDB, rsName string, awsAccessKeyId string, awsSecretAccessKey string, serviceDNSNames []string) ([]MaintainedMonarchComponents, error) {
	monarch := mdb.Spec.Monarch
	if monarch == nil {
		return nil, fmt.Errorf("monarch spec is nil")
	}

	// InitialMode: "ACTIVE" for active clusters, "STANDBY" for standby clusters
	initialMode := "STANDBY"
	if monarch.Role == mdbv1.MonarchRoleActive {
		initialMode = "ACTIVE"
	}

	mc := MaintainedMonarchComponents{
		ReplicaSetID:       rsName,
		ClusterPrefix:      monarch.ClusterPrefix,
		InitialMode:        initialMode,
		AWSBucketName:      monarch.S3BucketName,
		AWSRegion:          monarch.AWSRegion,
		AWSAccessKeyID:     awsAccessKeyId,
		AWSSecretAccessKey: awsSecretAccessKey,
		S3BucketEndpoint:   monarch.S3BucketEndpoint,
		S3PathStyleAccess:  monarch.S3PathStyleAccess,
	}

	if monarch.Role == mdbv1.MonarchRoleActive {
		// Active clusters use the active RS name and shipper version.
		mc.InjectorConfig = InjectorConfig{
			Version: monarch.ShipperVersion,
			Shards:  []InjectorShard{},
		}
	} else {
		// Standby clusters need injector instances pointing at each member's Service DNS.
		activeRSName := monarch.ActiveReplicaSetId
		if activeRSName == "" {
			activeRSName = rsName
		}
		mc.ReplicaSetID = activeRSName

		instances := make([]InjectorInstance, len(serviceDNSNames))
		for i, dns := range serviceDNSNames {
			instances[i] = InjectorInstance{
				ID:                 i,
				Hostname:           dns,
				Port:               9995,
				ExternallyManaged:  true,
				HealthAPIEndpoint:  dns + ":8080",
				MonarchAPIEndpoint: dns + ":1122",
			}
		}

		mc.InjectorConfig = InjectorConfig{
			Version: monarch.InjectorVersion,
			Shards: []InjectorShard{
				{
					ShardID:     "0",
					ReplSetName: rsName,
					Instances:   instances,
				},
			},
		}
	}

	return []MaintainedMonarchComponents{mc}, nil
}
