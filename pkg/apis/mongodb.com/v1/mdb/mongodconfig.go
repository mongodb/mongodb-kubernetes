package mdb

import (
	"strings"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/maputil"
	"go.uber.org/zap"
)

type AdditionalMongodConfig map[string]interface{}

func NewEmptyAdditionalMongodConfig() AdditionalMongodConfig {
	return make(map[string]interface{}, 0)
}
func NewAdditionalMongodConfig(key string, value interface{}) AdditionalMongodConfig {
	config := NewEmptyAdditionalMongodConfig()
	config.AddOption(key, value)
	return config
}

func (c AdditionalMongodConfig) AddOption(key string, value interface{}) AdditionalMongodConfig {
	keys := strings.Split(key, ".")
	maputil.SetMapValue(c, value, keys...)
	return c
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
