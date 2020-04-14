package v1

import "reflect"

type StatusOption interface {
	value() interface{}
}

type noOption struct{}

func (o noOption) value() interface{} {
	return nil
}

type MessageOption struct {
	Message string
}

func NewMessageOption(message string) MessageOption {
	return MessageOption{Message: message}
}

func (o MessageOption) value() interface{} {
	return o.Message
}

type WarningsOption struct {
	Warnings []StatusWarning
}

func NewWarningsOption(warnings []StatusWarning) WarningsOption {
	return WarningsOption{Warnings: warnings}
}

func (o WarningsOption) value() interface{} {
	return o.Warnings
}

type BaseUrlOption struct {
	BaseUrl string
}

func NewBaseUrlOption(baseUrl string) BaseUrlOption {
	return BaseUrlOption{BaseUrl: baseUrl}
}

func (o BaseUrlOption) value() interface{} {
	return o.BaseUrl
}

type OMStatusPartOption struct {
	StatusPart StatusPart
}

func NewOMStatusPartOption(statusPart StatusPart) OMStatusPartOption {
	return OMStatusPartOption{StatusPart: statusPart}
}

func (o OMStatusPartOption) value() interface{} {
	return o.StatusPart
}

func GetStatusOption(statusOptions []StatusOption, targetOption StatusOption) (StatusOption, bool) {
	for _, s := range statusOptions {
		if reflect.TypeOf(s) == reflect.TypeOf(targetOption) {
			return s, true
		}
	}
	return noOption{}, false
}
