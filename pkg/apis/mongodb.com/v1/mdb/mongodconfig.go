package mdb

import (
	"sort"
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

// ToFlatList returns all mongodb options as a sorted list of string values.
// It performs a recursive traversal of maps and dumps the current config to the final list of configs
func (c AdditionalMongodConfig) ToFlatList() []string {
	result := traverse(c.ToMap(), []string{})
	sort.Strings(result)
	return result
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

func traverse(currentValue interface{}, currentPath []string) []string {
	switch v := currentValue.(type) {
	case map[string]interface{}:
		{
			allPaths := []string{}
			for key, value := range v {
				allPaths = append(allPaths, traverse(value, append(currentPath, key))...)
			}
			return allPaths
		}
	default:
		{
			// We found the "terminal" node in the map - need to dump the current path
			path := strings.Join(currentPath, ".")
			return []string{path}
		}
	}
}
