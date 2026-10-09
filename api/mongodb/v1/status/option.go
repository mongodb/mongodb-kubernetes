package status

import (
	"reflect"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type Option interface {
	Value() any
}

type noOption struct{}

func (o noOption) Value() any {
	return nil
}

// MessageOption describes the status message
type MessageOption struct {
	Message string
}

func NewMessageOption(message string) MessageOption {
	return MessageOption{Message: message}
}

func (o MessageOption) Value() any {
	return o.Message
}

// WarningsOption describes the status warnings
type WarningsOption struct {
	Warnings []Warning
}

func NewWarningsOption(warnings []Warning) WarningsOption {
	return WarningsOption{Warnings: warnings}
}

func (o WarningsOption) Value() any {
	return o.Warnings
}

// BaseUrlOption describes the Ops Manager base URL.
type BaseUrlOption struct {
	BaseUrl string
}

func NewBaseUrlOption(baseUrl string) BaseUrlOption {
	return BaseUrlOption{BaseUrl: baseUrl}
}

func (o BaseUrlOption) Value() any {
	return o.BaseUrl
}

// ProjectIdOption describes the Ops Manager project (group) ID.
type ProjectIdOption struct {
	ProjectId string
}

func NewProjectIdOption(projectId string) ProjectIdOption {
	return ProjectIdOption{ProjectId: projectId}
}

func (o ProjectIdOption) Value() any {
	return o.ProjectId
}

// OMPartOption describes the part of Ops Manager resource status to be updated
type OMPartOption struct {
	StatusPart Part
}

func NewOMPartOption(statusPart Part) OMPartOption {
	return OMPartOption{StatusPart: statusPart}
}

func (o OMPartOption) Value() any {
	return o.StatusPart
}

// ResourcesNotReadyOption describes the resources dependent on the resource which are not ready
type ResourcesNotReadyOption struct {
	ResourcesNotReady []ResourceNotReady
}

func NewResourcesNotReadyOption(resourceNotReady []ResourceNotReady) ResourcesNotReadyOption {
	return ResourcesNotReadyOption{ResourcesNotReady: resourceNotReady}
}

func (o ResourcesNotReadyOption) Value() any {
	return o.ResourcesNotReady
}

type BackupStatusOption struct {
	statusName string
}

func NewBackupStatusOption(statusName string) BackupStatusOption {
	return BackupStatusOption{
		statusName: statusName,
	}
}

func (o BackupStatusOption) Value() any {
	return o.statusName
}

func GetOption(statusOptions []Option, targetOption Option) (Option, bool) {
	for _, s := range statusOptions {
		if reflect.TypeOf(s) == reflect.TypeOf(targetOption) {
			return s, true
		}
	}
	return noOption{}, false
}

// PVCStatusOption describes the resources pvc statuses
type PVCStatusOption struct {
	PVC *PVC
}

func NewPVCsStatusOption(pvc *PVC) PVCStatusOption {
	return PVCStatusOption{PVC: pvc}
}

func (o PVCStatusOption) Value() any {
	return o.PVC
}

// NewPVCsStatusOptionEmptyStatus sets a nil status; such that later in r.updateStatus(), commonUpdate sets the field
// explicitly to nil to remove that field.
// Otherwise, that field will forever be in the status field.
func NewPVCsStatusOptionEmptyStatus() PVCStatusOption {
	return PVCStatusOption{PVC: nil}
}

// MigrationStatusOption writes a connectivity (or other) condition into status.conditions.
// Phase and observed external-member counts are computed automatically in UpdateStatus via applyComputedReplicaSetMigrationStatus.
type MigrationStatusOption struct {
	Condition metav1.Condition
}

// NewMigrationStatusOptionWithCondition returns an option that merges condition into status.conditions.
func NewMigrationStatusOptionWithCondition(condition metav1.Condition) MigrationStatusOption {
	return MigrationStatusOption{Condition: condition}
}

func (o MigrationStatusOption) Value() any {
	return o.Condition
}
