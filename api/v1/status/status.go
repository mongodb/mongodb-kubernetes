package status

import (
	"reflect"

	"github.com/10gen/ops-manager-kubernetes/api/v1/status/pvc"

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
	GetCommonStatus(options ...Option) *Common
}

// Common is the struct shared by all statuses in existing Custom Resources.
// +kubebuilder:object:generate:=true
type Common struct {
	Phase              Phase              `json:"phase"`
	Message            string             `json:"message,omitempty"`
	LastTransition     string             `json:"lastTransition,omitempty"`
	ResourcesNotReady  []ResourceNotReady `json:"resourcesNotReady,omitempty"`
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
	PVCs               PVCS               `json:"pvc,omitempty"`
}

type PVCS []PVC

func (p *PVCS) Merge(pvc2 PVC) PVCS {
	if p == nil {
		return nil
	}

	found := false
	for i := range *p {
		if (*p)[i].StatefulsetName == pvc2.StatefulsetName {
			found = true
			(*p)[i].Phase = pvc2.Phase
		}
	}

	if !found {
		*p = append(*p, pvc2)
	}

	return *p
}

type PVC struct {
	Phase           pvc.Phase `json:"phase"`
	StatefulsetName string    `json:"statefulsetName"`
}

func (p *PVC) GetPhase() pvc.Phase {
	if p == nil || p.StatefulsetName == "" || p.Phase == "" {
		return pvc.PhaseNoAction
	}
	return p.Phase
}

// UpdateCommonFields is the update function to update common fields used in statuses of all managed CRs
func (s *Common) UpdateCommonFields(phase Phase, generation int64, statusOptions ...Option) {
	previousStatus := s.DeepCopy()
	s.Phase = phase
	s.ObservedGeneration = generation
	if option, exists := GetOption(statusOptions, MessageOption{}); exists {
		s.Message = stringutil.UpperCaseFirstChar(option.(MessageOption).Message)
	}
	if option, exists := GetOption(statusOptions, ResourcesNotReadyOption{}); exists {
		s.ResourcesNotReady = option.(ResourcesNotReadyOption).ResourcesNotReady
	}
	if option, exists := GetOption(statusOptions, PVCStatusOption{}); exists {
		p := option.(PVCStatusOption).PVC
		if p == nil {
			s.PVCs = nil
		} else {
			s.PVCs.Merge(*p)
		}
	}
	// We update the time only if the status really changed. Otherwise, we'd like to preserve the old one.
	if !reflect.DeepEqual(previousStatus, s) {
		s.LastTransition = timeutil.Now()
	}
}
