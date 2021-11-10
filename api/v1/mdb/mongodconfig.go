package mdb

import (
	"encoding/json"
	"strings"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/maputil"
	"go.uber.org/zap"
)

// The CRD generator does not support map[string]interface{}
// on the top level and hence we need to work around this with
// a wrapping struct.

type AdditionalMongodConfig struct {
	Object map[string]interface{} `json:"-"`
}

// Note: The MarshalJSON and UnmarshalJSON need to be explicitly implemented in this case as our wrapper type itself cannot be marshalled/unmarshalled by default. Without this custom logic the values provided in the resource definition will not be set in the struct created.
// MarshalJSON defers JSON encoding to the wrapped map
func (m *AdditionalMongodConfig) MarshalJSON() ([]byte, error) {
	return json.Marshal(m.Object)
}

// UnmarshalJSON will decode the data into the wrapped map
func (m *AdditionalMongodConfig) UnmarshalJSON(data []byte) error {
	if m.Object == nil {
		m.Object = map[string]interface{}{}
	}
	return json.Unmarshal(data, &m.Object)
}

func NewEmptyAdditionalMongodConfig() AdditionalMongodConfig {
	return AdditionalMongodConfig{Object: make(map[string]interface{}, 0)}
}

func NewAdditionalMongodConfig(key string, value interface{}) AdditionalMongodConfig {
	config := NewEmptyAdditionalMongodConfig()
	config.AddOption(key, value)
	return config
}

func (c AdditionalMongodConfig) AddOption(key string, value interface{}) AdditionalMongodConfig {
	keys := strings.Split(key, ".")
	maputil.SetMapValue(c.Object, value, keys...)
	return c
}

// ToFlatList returns all mongodb options as a sorted list of string values.
// It performs a recursive traversal of maps and dumps the current config to the final list of configs
func (c AdditionalMongodConfig) ToFlatList() []string {
	return maputil.ToFlatList(c.ToMap())
}

// GetPortOrDefault returns the port that should be used for the mongo process.
// if no port is specified in the additional mongo args, the default
// port of 27017 will be used
func (c AdditionalMongodConfig) GetPortOrDefault() int32 {
	if c.Object == nil {
		return util.MongoDbDefaultPort
	}

	// https://golang.org/pkg/encoding/json/#Unmarshal
	// the port will be stored as a float64.
	// However, on unit tests, and because of the way the deserialization
	// works, this value is returned as an int. That's why we read the
	// port as Int which uses the `cast` library to cast both float32 and int
	// types into Int.
	port := maputil.ReadMapValueAsInt(c.Object, "net", "port")
	if port == 0 {
		return util.MongoDbDefaultPort
	}

	return int32(port)
}

// DeepCopy is defined manually as codegen utility cannot generate copy methods for 'interface{}'
func (in *AdditionalMongodConfig) DeepCopy() *AdditionalMongodConfig {
	if in == nil {
		return nil
	}
	out := new(AdditionalMongodConfig)
	in.DeepCopyInto(out)
	return out
}

func (in *AdditionalMongodConfig) DeepCopyInto(out *AdditionalMongodConfig) {
	cp, err := util.MapDeepCopy(in.Object)
	if err != nil {
		zap.S().Errorf("Failed to copy the map: %s", err)
		return
	}
	config := AdditionalMongodConfig{Object: cp}
	*out = config
}

// ToMap creates a copy of the config as a map (Go is quite restrictive to types and sometimes we need to
// explicitly declare the type as map :( )
func (c AdditionalMongodConfig) ToMap() map[string]interface{} {
	if c.Object == nil {
		return map[string]interface{}{}
	}
	cp, err := util.MapDeepCopy(c.Object)
	if err != nil {
		zap.S().Errorf("Failed to copy the map: %s", err)
		return nil
	}
	return cp
}
