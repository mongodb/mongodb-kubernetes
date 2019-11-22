package api

import "github.com/10gen/ops-manager-kubernetes/pkg/controller/om/model"

// Admin (imported as 'api.Admin') is the client to all "administrator" related operations with Ops Manager
// which do not relate to specific groups (that's why it's different from 'om.Connection'). The only state expected
// to be encapsulated is baseUrl, user and key
type Admin interface {
	ReadDaemonConfig(hostName, headDbDir string) (*model.DaemonConfig, error)
	CreateDaemonConfig(hostName, headDbDir string) error
}

// DefaultOmAdmin is the default (production) implementation of Admin interface
type DefaultOmAdmin struct {
	BaseURL      string
	User         string
	PublicAPIKey string
}

func NewOmAdmin(baseUrl, user, publicApiKey string) *DefaultOmAdmin {
	return &DefaultOmAdmin{BaseURL: baseUrl, User: user, PublicAPIKey: publicApiKey}
}

func (a *DefaultOmAdmin) ReadDaemonConfig(hostName, headDbDir string) (*model.DaemonConfig, error) {
	// TODO use http.go
	return nil, nil
}
