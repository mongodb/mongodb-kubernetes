package operator

import (
	"os"
	"testing"

	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/stretchr/testify/assert"
)

func TestGetWatchedNamespace(t *testing.T) {
	t.Setenv(util.WatchNamespace, "one-namespace")
	assert.Equal(t, []string{"one-namespace"}, GetWatchedNamespace())

	t.Setenv(util.WatchNamespace, "one-namespace, two-namespace,three-namespace")
	assert.Equal(t, []string{"one-namespace", "two-namespace", "three-namespace"}, GetWatchedNamespace())

	t.Setenv(util.WatchNamespace, "")
	assert.Equal(t, []string{""}, GetWatchedNamespace())

	t.Setenv(util.WatchNamespace, ",")
	assert.Equal(t, []string{OperatorNamespace}, GetWatchedNamespace())

	t.Setenv(util.WatchNamespace, ",one-namespace")
	assert.Equal(t, []string{"one-namespace"}, GetWatchedNamespace())

	t.Setenv(util.WatchNamespace, "*")
	assert.Equal(t, []string{""}, GetWatchedNamespace())

	t.Setenv(util.WatchNamespace, "*,hi")
	assert.Equal(t, []string{""}, GetWatchedNamespace())

	os.Unsetenv(util.WatchNamespace)
	assert.Equal(t, []string{""}, GetWatchedNamespace())
}
