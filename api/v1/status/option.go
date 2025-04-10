package status

import (
	"reflect"
)

type Option interface {
	Value() interface{}
}

type noOption struct{}

func (o noOption) Value() interface{} {
	return nil
}

// MessageOption describes the status message
type MessageOption struct {
	Message string
}

func NewMessageOption(message string) MessageOption {
	return MessageOption{Message: message}
}

func (o MessageOption) Value() interface{} {
	return o.Message
}

// WarningsOption describes the status warnings
type WarningsOption struct {
	Warnings []Warning
}

func NewWarningsOption(warnings []Warning) WarningsOption {
	return WarningsOption{Warnings: warnings}
}

func (o WarningsOption) Value() interface{} {
	return o.Warnings
}

// BaseUrlOption describes the Ops Manager base URL.
type BaseUrlOption struct {
	BaseUrl string
}

func NewBaseUrlOption(baseUrl string) BaseUrlOption {
	return BaseUrlOption{BaseUrl: baseUrl}
}

func (o BaseUrlOption) Value() interface{} {
	return o.BaseUrl
}

// OMPartOption describes the part of Ops Manager resource status to be updated
type OMPartOption struct {
	StatusPart Part
}

func NewOMPartOption(statusPart Part) OMPartOption {
	return OMPartOption{StatusPart: statusPart}
}

func (o OMPartOption) Value() interface{} {
	return o.StatusPart
}

// ResourcesNotReadyOption describes the resources dependent on the resource which are not ready
type ResourcesNotReadyOption struct {
	ResourcesNotReady []ResourceNotReady
}

func NewResourcesNotReadyOption(resourceNotReady []ResourceNotReady) ResourcesNotReadyOption {
	return ResourcesNotReadyOption{ResourcesNotReady: resourceNotReady}
}

func (o ResourcesNotReadyOption) Value() interface{} {
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

func (o BackupStatusOption) Value() interface{} {
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

func (o PVCStatusOption) Value() interface{} {
	return o.PVC
}

// NewPVCsStatusOptionEmptyStatus sets a nil status; such that later in r.updateStatus(), commonUpdate sets the field
// explicitly to nil to remove that field.
// Otherwise, that field will forever be in the status field.
func NewPVCsStatusOptionEmptyStatus() PVCStatusOption {
	return PVCStatusOption{PVC: nil}
}
