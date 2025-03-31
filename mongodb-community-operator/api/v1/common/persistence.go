package common

type Persistence struct {
	SingleConfig   *PersistenceConfig         `json:"single,omitempty"`
	MultipleConfig *MultiplePersistenceConfig `json:"multiple,omitempty"`
}

type MultiplePersistenceConfig struct {
	Data    *PersistenceConfig `json:"data,omitempty"`
	Journal *PersistenceConfig `json:"journal,omitempty"`
	Logs    *PersistenceConfig `json:"logs,omitempty"`
}

type PersistenceConfig struct {
	Storage      string  `json:"storage,omitempty"`
	StorageClass *string `json:"storageClass,omitempty"`

	// +kubebuilder:pruning:PreserveUnknownFields
	LabelSelector *LabelSelectorWrapper `json:"labelSelector,omitempty"`
}
