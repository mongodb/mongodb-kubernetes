package controller

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGetCRDsToWatch(t *testing.T) {
	tests := []struct {
		name            string
		watchCRDsString string
		expected        []string
	}{
		{
			"All 3 CRDs",
			"mongodb,mongodbusers,opsmanagers",
			[]string{"mongodb", "mongodbusers", "opsmanagers"},
		},
		{
			"2 of 3 CRDs",
			"mongodb,opsmanagers",
			[]string{"mongodb", "opsmanagers"},
		},
		{
			"1 of 3 CRDs",
			"opsmanagers",
			[]string{"opsmanagers"},
		},
		{
			"The Empty String",
			"",
			[]string{"mongodb", "mongodbusers", "opsmanagers"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := getCRDsToWatch(tt.watchCRDsString)
			assert.True(t, reflect.DeepEqual(actual, tt.expected), "getCRDsToWatch() = %v, expected %v", actual, tt.expected)
		})
	}
}
