package mdb

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDeepCopy(t *testing.T) {
	config := NewAdditionalMongodConfig("first.second", "value")
	cp := *config.DeepCopy()

	expectedAdditionalConfig := AdditionalMongodConfig{
		object: map[string]interface{}{"first": map[string]interface{}{"second": "value"}},
	}
	assert.Equal(t, expectedAdditionalConfig.object, cp.object)

	cp.object["first"].(map[string]interface{})["second"] = "newvalue"

	// The value in the first config hasn't changed
	assert.Equal(t, "value", config.object["first"].(map[string]interface{})["second"])
}

func TestToFlatList(t *testing.T) {
	config := NewAdditionalMongodConfig("one.two.three", "v1")
	config.AddOption("one.two.four", 5)
	config.AddOption("one.five", true)
	config.AddOption("six.seven.eight", "v2")
	config.AddOption("six.nine", "v3")

	list := config.ToFlatList()

	expectedStrings := []string{"one.five", "one.two.four", "one.two.three", "six.nine", "six.seven.eight"}
	assert.Equal(t, expectedStrings, list)
}
