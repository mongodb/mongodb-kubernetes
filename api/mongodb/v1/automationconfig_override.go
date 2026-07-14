package v1

import (
	"github.com/mongodb/mongodb-kubernetes/pkg/automationconfig"
)

// AutomationConfigOverride contains fields which will be overridden in the operator created config.
type AutomationConfigOverride struct {
	Processes  []OverrideProcess  `json:"processes,omitempty"`
	ReplicaSet OverrideReplicaSet `json:"replicaSet,omitempty"`
}

// OverrideReplicaSet holds replica set override fields for the AutomationConfig.
type OverrideReplicaSet struct {
	// Id can be used together with additionalMongodConfig.replication.replSetName
	// to manage clusters where replSetName differs from the MongoDBCommunity resource name
	Id *string `json:"id,omitempty"`
	// +kubebuilder:validation:Type=object
	// +kubebuilder:pruning:PreserveUnknownFields
	Settings MapWrapper `json:"settings,omitempty"`
}

// OverrideProcess contains fields that we can override on the AutomationConfig processes.
type OverrideProcess struct {
	Name      string                         `json:"name"`
	Disabled  bool                           `json:"disabled"`
	LogRotate *automationconfig.CrdLogRotate `json:"logRotate,omitempty"`
}
