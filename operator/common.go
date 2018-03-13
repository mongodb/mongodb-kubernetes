package operator

// This is a collection of some utility/common methods that may be shared by other go source code

import (
	"errors"
	"fmt"
	"os"
	"reflect"
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
		User:         os.Getenv("EMAIL"),
		GroupId:      os.Getenv("GROUP_ID"),
	}
}

// NewOpsManagerConnectionFromEnv is the convenience method creating the connection object from environment variable
// This is definitely not a final solution, just to make operator's code shorter for a while
func NewOpsManagerConnectionFromEnv() *om.OmConnection {
	omConfig := GetOpsManagerConfig()
	return om.NewOpsManagerConnection(omConfig.BaseUrl, omConfig.GroupId, omConfig.User, omConfig.PublicApiKey)
}

// AttributeUpdate is just a mock of how a attribute can be declared as updated from an
// old value to a new value. The values should be interfaces and we'll have to reflect on them.
// Or hard-code the names and types of expected values in a very go idiomatic way.
type AttributeUpdate struct {
	AttributeName string
	OldValue      interface{}
	NewValue      interface{}
}

func GetResourceUpdates(oldObj, newObj interface{}) ([]AttributeUpdate, error) {
	oldObjType := reflect.TypeOf(oldObj)
	newObjType := reflect.TypeOf(newObj)

	if oldObjType != newObjType {
		// this should not happen
		return nil, errors.New("Object are not the same type!")
	}
	if reflect.TypeOf(oldObj) == reflect.TypeOf(MongoDbStandalone) {
		fmt.Println("It is a standalone!")
	}

	return []AttributeUpdate{}, nil
}
