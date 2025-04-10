package backup

// GroupConfigReader reads the Group Backup Config
type GroupConfigReader interface {
	// ReadGroupBackupConfig reads project level backup configuration
	// See: https://www.mongodb.com/docs/ops-manager/v6.0/reference/api/admin/backup/groups/get-one-backup-group-configuration-by-id/
	ReadGroupBackupConfig() (GroupBackupConfig, error)
}

// GroupConfigUpdater updates the existing Group Backup Config
type GroupConfigUpdater interface {
	// UpdateGroupBackupConfig updates project level backup configuration
	// See: https://www.mongodb.com/docs/ops-manager/v6.0/reference/api/admin/backup/groups/update-one-backup-group-configuration/
	UpdateGroupBackupConfig(config GroupBackupConfig) ([]byte, error)
}

// DaemonFilter corresponds to the "daemonFilter" from the "Project Backup Jobs Configuration" from Ops Manager
// See: https://www.mongodb.com/docs/ops-manager/v6.0/reference/api/admin/backup/groups/update-one-backup-group-configuration/
type DaemonFilter struct {
	HeadRootDirectory *string `json:"headRootDirectory,omitempty"`
	Machine           *string `json:"machine,omitempty"`
}

// OplogStoreFilter corresponds to the "oplogStoreFilter" from the "Project Backup Jobs Configuration" from Ops Manager
// See: https://www.mongodb.com/docs/ops-manager/v6.0/reference/api/admin/backup/groups/update-one-backup-group-configuration/
type OplogStoreFilter struct {
	Id   *string `json:"id,omitempty"`
	Type *string `json:"type,omitempty"`
}

// SnapshotStoreFilter corresponds to the "snapshotStoreFilter" from the "Project Backup Jobs Configuration" from Ops Manager
// See: https://www.mongodb.com/docs/ops-manager/v6.0/reference/api/admin/backup/groups/update-one-backup-group-configuration/
type SnapshotStoreFilter struct {
	OplogStoreFilter `json:",inline"`
}

// GroupBackupConfig corresponds to the "Project Backup Jobs Configuration" from Ops Manager
// See: https://www.mongodb.com/docs/ops-manager/v6.0/reference/api/admin/backup/groups/update-one-backup-group-configuration/
type GroupBackupConfig struct {
	DaemonFilter           []DaemonFilter        `json:"daemonFilter,omitempty"`
	Id                     *string               `json:"id,omitempty"`
	KmipClientCertPassword *string               `json:"kmipClientCertPassword,omitempty"`
	KmipClientCertPath     *string               `json:"kmipClientCertPath,omitempty"`
	LabelFilter            []string              `json:"labelFilter"`
	OplogStoreFilter       []OplogStoreFilter    `json:"oplogStoreFilter,omitempty"`
	SnapshotStoreFilter    []SnapshotStoreFilter `json:"snapshotStoreFilter,omitempty"`
	SyncStoreFilter        []string              `json:"syncStoreFilter,omitempty"`
}
