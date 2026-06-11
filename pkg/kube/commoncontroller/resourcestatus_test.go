package commoncontroller

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStatusSubresourcePatchPaths(t *testing.T) {
	tests := []struct {
		name     string
		fullPath string
		want     []string
	}{
		{name: "status root", fullPath: "/status", want: []string{"/status"}},
		{name: "nested substatus", fullPath: "/status/loadBalancer", want: []string{"/status", "/status/loadBalancer"}},
		{name: "deeply nested substatus", fullPath: "/status/opsManager/backup", want: []string{"/status", "/status/opsManager", "/status/opsManager/backup"}},
		{name: "empty path", fullPath: "", want: nil},
		{name: "bare slash never patches the document root", fullPath: "/", want: nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := statusSubresourcePatchPaths(tc.fullPath)
			assert.Equal(t, tc.want, got)
			assert.NotContains(t, got, "/", "a JSON-patch add at the root replaces the entire status")
		})
	}
}
