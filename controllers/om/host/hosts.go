package host

import "context"

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
	GetHosts(ctx context.Context) (*Result, error)
}

type Adder interface {
	AddHost(ctx context.Context, host Host) error
}

type Updater interface {
	UpdateHost(ctx context.Context, host Host) error
}

type Remover interface {
	RemoveHost(ctx context.Context, hostID string) error
}

type GetRemover interface {
	Getter
	Remover
}

type GetAdder interface {
	Getter
	Adder
}
