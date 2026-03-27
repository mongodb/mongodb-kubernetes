package search

import "github.com/mongodb/mongodb-kubernetes/api/v1/status"

type MongoDBSearchVersionOption struct {
	Version string
}

var _ status.Option = MongoDBSearchVersionOption{}

func NewMongoDBSearchVersionOption(version string) MongoDBSearchVersionOption {
	return MongoDBSearchVersionOption{Version: version}
}

func (o MongoDBSearchVersionOption) Value() interface{} {
	return o.Version
}

// SearchPart identifies which sub-status of MongoDBSearch to update.
// Search-scoped to avoid polluting the shared status.Part enum.
type SearchPart int

const (
	// SearchPartLoadBalancer targets status.loadBalancer.
	SearchPartLoadBalancer SearchPart = iota
)

// SearchPartOption tells UpdateStatus/GetStatus/GetStatusPath which
// sub-status to operate on. Analogous to status.OMPartOption.
type SearchPartOption struct {
	Part SearchPart
}

var _ status.Option = SearchPartOption{}

func NewSearchPartOption(part SearchPart) SearchPartOption {
	return SearchPartOption{Part: part}
}

func (o SearchPartOption) Value() interface{} {
	return o.Part
}
