package v1

type StatusWarning string

type StatusWarnings []StatusWarning

const (
	MultipleClustersInProjectWarning StatusWarning = "Project contains multiple clusters. Please see documentation here: https://dochub.mongodb.org/core/kubernetes-v1.3-upgrade"
	CouldNotRemoveTagsWarning        StatusWarning = "Could not remove tags from project"
	SEP                              StatusWarning = ";"
)

func (m StatusWarnings) AddIfNotExists(warning StatusWarning) StatusWarnings {
	for _, existingWarning := range m {
		if existingWarning == warning || existingWarning == warning+SEP {
			return m
		}
	}

	// separate warnings by a ;
	for i := 0; i < len(m); i++ {
		existingWarning := m[i]
		if existingWarning[len(existingWarning)-1:] != SEP {
			m[i] += SEP
		}
	}

	return append(m, warning)
}
