package mdb

import (
	"strings"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"go.uber.org/zap"
)

type AdditionalMongodConfig map[string]interface{}

func NewEmptyAdditionalMongodConfig() AdditionalMongodConfig {
	return make(map[string]interface{}, 0)
}
func NewAdditionalMongodConfig(key, value string) AdditionalMongodConfig {
	keys := strings.Split(key, ".")
	config := NewEmptyAdditionalMongodConfig()
	current := config
	for _, k := range keys[0 : len(keys)-1] {
		current[k] = make(map[string]interface{})
		current = current[k].(map[string]interface{})
	}
	last := keys[len(keys)-1]
	current[last] = value
	return config
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
	cp, err := util.MapDeepCopy(*in)
	if err != nil {
		zap.S().Errorf("Failed to copy the map: %s", err)
		return
	}
	config := AdditionalMongodConfig(cp)
	*out = config
}

// ToMap creates a copy of the config as a map (Go is quite restrictive to types and sometimes we need to
// explicitly declare the type as map :( )
func (c AdditionalMongodConfig) ToMap() map[string]interface{} {
	cp, err := util.MapDeepCopy(c)
	if err != nil {
		zap.S().Errorf("Failed to copy the map: %s", err)
		return nil
	}
	return cp
}
