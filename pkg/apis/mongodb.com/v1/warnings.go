package v1

type StatusWarning string

type StatusWarnings []StatusWarning

const (
	MismatchedProjectNameWarning     StatusWarning = "Project name should be the same as the resource name"
	MultipleClustersInProjectWarning StatusWarning = "Project contains multiple clusters"
)
