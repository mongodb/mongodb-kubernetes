package mongod

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDeepCopy(t *testing.T) {
	config := NewAdditionalMongodConfig("first.second", "value")
	cp := *config.DeepCopy()

	assert.Equal(t, AdditionalMongodConfig{"first": map[string]interface{}{"second": "value"}}, cp)
	cp["first"].(map[string]interface{})["second"] = "newvalue"

	// The value in the first config hasn't changed
	assert.Equal(t, "value", config["first"].(map[string]interface{})["second"])
}
