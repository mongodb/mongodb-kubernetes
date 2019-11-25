package backup

type DaemonConfig struct {
	Machine MachineConfig `json:"machine"`
}

type MachineConfig struct {
	HeadRootDirectory string `json:"headRootDirectory"`
	MachineHostName   string `json:"machine"`
}
