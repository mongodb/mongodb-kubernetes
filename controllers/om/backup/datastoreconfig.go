package backup

import (
	"fmt"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"
)

type DataStoreConfigResponse struct {
	DataStoreConfigs []DataStoreConfig `json:"results"`
}

// DataStoreConfig corresponds to 'ApiBackupDataStoreConfigView' in mms. It's shared by all configs which relate to
// MongoDB (oplogStore, blockStore)
type DataStoreConfig struct {
	// These are the fields managed by the Operator
	Id     string `json:"id"`
	Uri    string `json:"uri"`
	UseSSL bool   `json:"ssl"`

	// These are all the rest fields
	LoadFactor           int      `json:"loadFactor,omitempty"`
	WriteConcern         string   `json:"writeConcern,omitempty"`
	UsedSize             int64    `json:"usedSize,omitempty"`
	AssignmentEnabled    bool     `json:"assignmentEnabled,omitempty"`
	MaxCapacityGB        int64    `json:"maxCapacityGB,omitempty"`
	Labels               []string `json:"labels"`
	EncryptedCredentials bool     `json:"encryptedCredentials,omitempty"`
}

// NewDataStoreConfig returns the new 'DataStoreConfig' object initializing the default values
func NewDataStoreConfig(id, uri string, tls bool, assignmentLabels []string) DataStoreConfig {
	ret := DataStoreConfig{
		Id:     id,
		Uri:    uri,
		UseSSL: tls,

		// Default values
		AssignmentEnabled: true,
	}

	// The assignment labels has been set in the CR - so the CR becomes a source of truth for them
	if assignmentLabels != nil {
		ret.Labels = assignmentLabels
	}

	return ret
}

func (s DataStoreConfig) Identifier() interface{} {
	return s.Id
}

// MergeIntoOpsManagerConfig performs the merge operation of the Operator config view ('s') into the OM owned one
// ('opsManagerConfig')
func (s DataStoreConfig) MergeIntoOpsManagerConfig(opsManagerConfig DataStoreConfig) DataStoreConfig {
	opsManagerConfig.Id = s.Id
	opsManagerConfig.Uri = s.Uri
	opsManagerConfig.UseSSL = s.UseSSL
	opsManagerConfig.Labels = s.Labels
	return opsManagerConfig
}

func (s DataStoreConfig) String() string {
	return fmt.Sprintf("id: %s, uri: %s, ssl: %v", s.Id, util.RedactMongoURI(s.Uri), s.UseSSL)
}
