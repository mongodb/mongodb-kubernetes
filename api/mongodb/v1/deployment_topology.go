package v1

// DeploymentTopology values are shared by the MongoDB CRD and Ops Manager telemetry mappings.
type DeploymentTopology string

const (
	DeploymentTopologySingleCluster DeploymentTopology = "SingleCluster"
	DeploymentTopologyMultiCluster  DeploymentTopology = "MultiCluster"
)
