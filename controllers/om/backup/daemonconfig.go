package backup

type DaemonConfig struct {
	Machine                     MachineConfig `json:"machine"`
	AssignmentEnabled           bool          `json:"assignmentEnabled"`
	BackupJobsEnabled           bool          `json:"backupJobsEnabled"`
	ResourceUsageEnabled        bool          `json:"resourceUsageEnabled"`
	GarbageCollectionEnabled    bool          `json:"garbageCollectionEnabled"`
	RestoreQueryableJobsEnabled bool          `json:"restoreQueryableJobsEnabled"`
	Configured                  bool          `json:"configured"`
	Labels                      []string      `json:"labels"`
}

type MachineConfig struct {
	HeadRootDirectory string `json:"headRootDirectory"`
	MachineHostName   string `json:"machine"`
}

// NewDaemonConfig creates the 'DaemonConfig' fully initialized
func NewDaemonConfig(hostName, headDbDir string, assignmentLabels []string) DaemonConfig {
	return DaemonConfig{
		Machine: MachineConfig{
			HeadRootDirectory: headDbDir,
			MachineHostName:   hostName,
		},
		AssignmentEnabled:           true,
		Labels:                      assignmentLabels,
		BackupJobsEnabled:           true,
		ResourceUsageEnabled:        true,
		GarbageCollectionEnabled:    true,
		RestoreQueryableJobsEnabled: true,
		// TODO is this ok to have daemon configured with may be lacking oplog stores for example?
		Configured: true,
	}
}
