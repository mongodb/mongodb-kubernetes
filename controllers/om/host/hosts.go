package host

type Host struct {
	Password          string `json:"password"`
	Username          string `json:"username"`
	Hostname          string `json:"hostname"`
	Port              int    `json:"port"`
	AuthMechanismName string `json:"authMechanismName"`
	Id                string `json:"id"`
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

// Contains accepts a slice of Hosts and a host to look for
// it returns true if the host is present in the slice otherwise false
func Contains(hosts []Host, host Host) bool {
	for _, h := range hosts {
		if h.Hostname == host.Hostname {
			return true
		}
	}
	return false
}
