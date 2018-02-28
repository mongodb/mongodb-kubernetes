package main

const (
	ContainerName            = "ops-manager-agent"
	ContainerImage           = "ops-manager-agent"
	ContainerImagePullPolicy = "Never"

	ContainerConfigMapName = "ops-manager-config"

	MongoDbStandalone     = "MongoDbStandalone"
	MongoDbReplicaSet     = "MongoDbReplicaSet"
	MongoDbShardedCluster = "MongoDbShardedCluster"

	ResourceName      = "MongoDB"
	StandaloneMembers = 1

	OpsManagerAgentsResource = "/api/public/v1.0/groups/%s/agents/AUTOMATION"
)
