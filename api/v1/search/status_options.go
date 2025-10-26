package search

import "github.com/mongodb/mongodb-kubernetes/api/v1/status"

// MongoDBSearchVersionOption captures the reconciled mongot version for status updates.
type MongoDBSearchVersionOption struct {
	Version string
}

var _ status.Option = MongoDBSearchVersionOption{}

// NewMongoDBSearchVersionOption constructs an option that updates MongoDBSearch status version.
func NewMongoDBSearchVersionOption(version string) MongoDBSearchVersionOption {
	return MongoDBSearchVersionOption{Version: version}
}

// Value implements the status.Option interface.
func (o MongoDBSearchVersionOption) Value() interface{} {
	return o.Version
}
