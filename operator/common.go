package operator

// This is a collection of some utility/common methods that may be shared by other go source code

import (
	"os"
	"github.com/10gen/ops-manager-kubernetes/om"
)

// MakeInReference is required to return a *int32, which can't be declared as a literal.
func MakeIntReference(i int32) *int32 {
	return &i
}

type OpsManagerConfig struct {
	BaseUrl      string
	PublicApiKey string
	User         string
	GroupId      string
}

func GetOpsManagerConfig() OpsManagerConfig {
	return OpsManagerConfig{
		BaseUrl:      os.Getenv("BASE_URL"),
		PublicApiKey: os.Getenv("PUBLIC_API_KEY"),
		User:         os.Getenv("USER_LOGIN"),
		GroupId:      os.Getenv("GROUP_ID"),
	}
}

// NewOpsManagerConnectionFromEnv is the convenience method creating the connection object from environment variable
// This is definitely not a final solution, just to make operator's code shorter for a while
func NewOpsManagerConnectionFromEnv() *om.OmConnection {
	omConfig := GetOpsManagerConfig()
	return om.NewOpsManagerConnection(omConfig.BaseUrl, omConfig.GroupId, omConfig.User, omConfig.PublicApiKey)
}
