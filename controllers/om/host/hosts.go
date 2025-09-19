package host

type Host struct {
	Password          string `json:"password"`
	Username          string `json:"username"`
	Hostname          string `json:"hostname"`
	Port              int    `json:"port"`
	AuthMechanismName string `json:"authMechanismName"`
	Id                string `json:"id"`
	ReplicaStateName  string `json:"replicaStateName"`
	ReplicaSetName    string `json:"replicaSetName"`
	TypeName          string `json:"typeName"`
}

type Result struct {
	Results []Host `json:"results"`
}

type Getter interface {
	GetHosts() (*Result, error)
}

type Adder interface {
	AddHost(host Host) error
}

type Updater interface {
	UpdateHost(host Host) error
}

type Remover interface {
	RemoveHost(hostID string) error
}

type GetRemover interface {
	Getter
	Remover
}

type GetAdder interface {
	Getter
	Adder
}
