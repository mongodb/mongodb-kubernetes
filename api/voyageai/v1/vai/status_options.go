package vai

import "github.com/mongodb/mongodb-kubernetes/api/v1/status"

type VoyageAIVersionOption struct {
	Version string
}

var _ status.Option = VoyageAIVersionOption{}

func NewVoyageAIVersionOption(version string) VoyageAIVersionOption {
	return VoyageAIVersionOption{Version: version}
}

func (o VoyageAIVersionOption) Value() interface{} {
	return o.Version
}
