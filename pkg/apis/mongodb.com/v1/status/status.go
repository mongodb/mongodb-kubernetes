package status

import (
	"github.com/10gen/ops-manager-kubernetes/pkg/util/stringutil"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/timeutil"
)

type Writer interface {
	// UpdateStatus updates the status of the object
	UpdateStatus(phase Phase, statusOptions ...Option)

	// SetWarnings sets the warnings for the object, the list of Options
	// provided indicate which Status subresource should be updated in the case
	// of AppDB, OpsManager and Backup
	SetWarnings([]Warning, ...Option)

	// GetStatusPath should return the path that should be used
	// to patch the Status object
	GetStatusPath(options ...Option) string
}
type Reader interface {
	// GetStatus returns the status of the object. The list of Options
	// provided indicates which subresource will be returned. AppDB, OM or Backup
	GetStatus(options ...Option) interface{}
}

// Common is the struct shared by all statuses in existing Custom Resources.
type Common struct {
	Phase              Phase              `json:"phase"`
	Message            string             `json:"message,omitempty"`
	LastTransition     string             `json:"lastTransition,omitempty"`
	ResourcesNotReady  []ResourceNotReady `json:"resourcesNotReady,omitempty"`
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
}

// UpdateCommonFields is the update function to update common fields used in statuses of all managed CRs
func (s *Common) UpdateCommonFields(phase Phase, generation int64, statusOptions ...Option) {
	s.Phase = phase
	s.LastTransition = timeutil.Now()
	s.ObservedGeneration = generation
	if option, exists := GetOption(statusOptions, MessageOption{}); exists {
		s.Message = stringutil.UpperCaseFirstChar(option.(MessageOption).Message)
	}
	if option, exists := GetOption(statusOptions, ResourcesNotReadyOption{}); exists {
		s.ResourcesNotReady = option.(ResourcesNotReadyOption).ResourcesNotReady
	}
}
