package v1

import "reflect"

type StatusOption interface {
	value() interface{}
}

type noOption struct{}

func (o noOption) value() interface{} {
	return nil
}

// MessageOption describes the status message
type MessageOption struct {
	Message string
}

func NewMessageOption(message string) MessageOption {
	return MessageOption{Message: message}
}

func (o MessageOption) value() interface{} {
	return o.Message
}

// WarningsOption describes the status warnings
type WarningsOption struct {
	Warnings []StatusWarning
}

func NewWarningsOption(warnings []StatusWarning) WarningsOption {
	return WarningsOption{Warnings: warnings}
}

func (o WarningsOption) value() interface{} {
	return o.Warnings
}

// BaseUrlOption describes the Ops Manager base URL.
type BaseUrlOption struct {
	BaseUrl string
}

func NewBaseUrlOption(baseUrl string) BaseUrlOption {
	return BaseUrlOption{BaseUrl: baseUrl}
}

func (o BaseUrlOption) value() interface{} {
	return o.BaseUrl
}

// OMStatusPartOption describes the part of Ops Manager resource status to be updated
type OMStatusPartOption struct {
	StatusPart StatusPart
}

func NewOMStatusPartOption(statusPart StatusPart) OMStatusPartOption {
	return OMStatusPartOption{StatusPart: statusPart}
}

func (o OMStatusPartOption) value() interface{} {
	return o.StatusPart
}

// ResourcesNotReadyOption describes the resources dependent on the resource which are not ready
type ResourcesNotReadyOption struct {
	ResourcesNotReady []ResourceNotReady
}

func NewResourcesNotReadyOption(resourceNotReady []ResourceNotReady) ResourcesNotReadyOption {
	return ResourcesNotReadyOption{ResourcesNotReady: resourceNotReady}
}

func (o ResourcesNotReadyOption) value() interface{} {
	return o.ResourcesNotReady
}

func GetStatusOption(statusOptions []StatusOption, targetOption StatusOption) (StatusOption, bool) {
	for _, s := range statusOptions {
		if reflect.TypeOf(s) == reflect.TypeOf(targetOption) {
			return s, true
		}
	}
	return noOption{}, false
}
