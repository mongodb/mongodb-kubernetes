package mdb

import (
	"encoding/json"
	"strings"

	"go.uber.org/zap"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/maputil"
)

// The CRD generator does not support map[string]interface{}
// on the top level and hence we need to work around this with
// a wrapping struct.

// AdditionalMongodConfig contains a private non exported object with a json tag.
// Because we implement the Json marshal and unmarshal interface, json is still able to convert this object into its write-type.
// Making this field private enables us to make sure we don't directly access this field, making sure it is always initialized.
// The space is on purpose to not generate the comment in the CRD.

type AdditionalMongodConfig struct {
	object map[string]interface{} `json:"-"`
}

// Note: The MarshalJSON and UnmarshalJSON need to be explicitly implemented in this case as our wrapper type itself cannot be marshalled/unmarshalled by default. Without this custom logic the values provided in the resource definition will not be set in the struct created.
// MarshalJSON defers JSON encoding to the wrapped map
func (amc *AdditionalMongodConfig) MarshalJSON() ([]byte, error) {
	return json.Marshal(amc.object)
}

// UnmarshalJSON will decode the data into the wrapped map
func (amc *AdditionalMongodConfig) UnmarshalJSON(data []byte) error {
	if amc.object == nil {
		amc.object = map[string]interface{}{}
	}
	return json.Unmarshal(data, &amc.object)
}

func NewEmptyAdditionalMongodConfig() *AdditionalMongodConfig {
	return &AdditionalMongodConfig{object: make(map[string]interface{})}
}

func NewAdditionalMongodConfig(key string, value interface{}) *AdditionalMongodConfig {
	config := NewEmptyAdditionalMongodConfig()
	config.AddOption(key, value)
	return config
}

func (amc *AdditionalMongodConfig) AddOption(key string, value interface{}) *AdditionalMongodConfig {
	keys := strings.Split(key, ".")
	maputil.SetMapValue(amc.object, value, keys...)
	return amc
}

// ToFlatList returns all mongodb options as a sorted list of string values.
// It performs a recursive traversal of maps and dumps the current config to the final list of configs
func (amc *AdditionalMongodConfig) ToFlatList() []string {
	return maputil.ToFlatList(amc.ToMap())
}

// GetPortOrDefault returns the port that should be used for the mongo process.
// if no port is specified in the additional mongo args, the default
// port of 27017 will be used
func (amc *AdditionalMongodConfig) GetPortOrDefault() int32 {
	if amc == nil || amc.object == nil {
		return util.MongoDbDefaultPort
	}

	// https://golang.org/pkg/encoding/json/#Unmarshal
	// the port will be stored as a float64.
	// However, on unit tests, and because of the way the deserialization
	// works, this value is returned as an int. That's why we read the
	// port as Int which uses the `cast` library to cast both float32 and int
	// types into Int.
	port := maputil.ReadMapValueAsInt(amc.object, "net", "port")
	if port == 0 {
		return util.MongoDbDefaultPort
	}

	return int32(port)
}

// DeepCopy is defined manually as codegen utility cannot generate copy methods for 'interface{}'
func (amc *AdditionalMongodConfig) DeepCopy() *AdditionalMongodConfig {
	if amc == nil {
		return nil
	}
	out := new(AdditionalMongodConfig)
	amc.DeepCopyInto(out)
	return out
}

func (amc *AdditionalMongodConfig) DeepCopyInto(out *AdditionalMongodConfig) {
	cp, err := util.MapDeepCopy(amc.object)
	if err != nil {
		zap.S().Errorf("Failed to copy the map: %s", err)
		return
	}
	config := AdditionalMongodConfig{object: cp}
	*out = config
}

// ToMap creates a copy of the config as a map (Go is quite restrictive to types, and sometimes we need to
// explicitly declare the type as map :( )
func (amc *AdditionalMongodConfig) ToMap() map[string]interface{} {
	if amc == nil || amc.object == nil {
		return map[string]interface{}{}
	}
	cp, err := util.MapDeepCopy(amc.object)
	if err != nil {
		zap.S().Errorf("Failed to copy the map: %s", err)
		return nil
	}
	return cp
}
