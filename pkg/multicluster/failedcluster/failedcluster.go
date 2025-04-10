package failedcluster

const (
	FailedClusterAnnotation       = "failedClusters"
	ClusterSpecOverrideAnnotation = "clusterSpecOverride"
)

type FailedCluster struct {
	ClusterName string
	Members     int
}
