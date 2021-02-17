package controlledfeature

import (
	"fmt"
	"testing"

	"github.com/10gen/ops-manager-kubernetes/pkg/util/versionutil"
	"github.com/stretchr/testify/assert"
)

func TestShouldUseFeatureControls(t *testing.T) {

	// older versions which do not support policy control
	assert.False(t, ShouldUseFeatureControls(toOMVersion("4.2.1")))
	assert.False(t, ShouldUseFeatureControls(toOMVersion("4.3.0")))

	// if we don't know the version, use the tag
	assert.False(t, ShouldUseFeatureControls(toOMVersion("")))

	// older version we don't know about, we assume a tag
	assert.False(t, ShouldUseFeatureControls(toOMVersion("3.6.0")))
	assert.False(t, ShouldUseFeatureControls(toOMVersion("3.6.2")))
	assert.False(t, ShouldUseFeatureControls(toOMVersion("3.6.3")))

	// minimum versions that support policy control
	assert.True(t, ShouldUseFeatureControls(toOMVersion("4.2.2")))
	assert.True(t, ShouldUseFeatureControls(toOMVersion("4.2.3")))
	assert.True(t, ShouldUseFeatureControls(toOMVersion("4.3.1")))
	assert.True(t, ShouldUseFeatureControls(toOMVersion("4.3.2")))
	assert.True(t, ShouldUseFeatureControls(toOMVersion("4.4.0")))
	assert.True(t, ShouldUseFeatureControls(toOMVersion("4.4.1")))
}

func toOMVersion(versionString string) versionutil.OpsManagerVersion {
	if versionString == "" {
		return versionutil.OpsManagerVersion{}
	}

	return versionutil.OpsManagerVersion{
		VersionString: fmt.Sprintf("%s.56729.20191105T2247Z", versionString),
	}
}
