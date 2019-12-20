package v1

type StatusWarning string

type StatusWarnings []StatusWarning

const (
	MultipleClustersInProjectWarning StatusWarning = "Project contains multiple clusters. Please see documentation here: https://dochub.mongodb.org/core/kubernetes-v1.3-upgrade"
	CouldNotRemoveTagsWarning        StatusWarning = "Could not remove tags from project"
	S3BackupsNotFullyConfigured      StatusWarning = "S3 configuration requires at least 1 Oplog Store configuration and at least 1 S3 store to be fully configured"
)
