package status

import (
	"github.com/10gen/ops-manager-kubernetes/pkg/util/stringutil"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/timeutil"
)

type Writer interface {
	// UpdateStatus updates the status of the object
	UpdateStatus(phase Phase, statusOptions ...Option)

	// SetWarnings sets the warnings for the object
	SetWarnings([]Warning)
}
type Reader interface {
	// GetStatus returns the status of the object
	GetStatus() interface{}
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
