package v1

type StatusWarning string

type StatusWarnings []StatusWarning

const (
	MultipleClustersInProjectWarning StatusWarning = "Project contains multiple clusters. Please see documentation here: https://dochub.mongodb.org/core/kubernetes-v1.3-upgrade"
	CouldNotRemoveTagsWarning        StatusWarning = "Could not remove tags from project"
)
