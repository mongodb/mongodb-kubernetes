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
