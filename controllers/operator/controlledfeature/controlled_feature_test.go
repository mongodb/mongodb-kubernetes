package controlledfeature

import (
	"fmt"
	"testing"

	"github.com/10gen/ops-manager-kubernetes/pkg/util/versionutil"
	"github.com/stretchr/testify/assert"
)

func TestShouldUseFeatureControls(t *testing.T) {

	// All OM versions that we support now support feature controls
	assert.True(t, ShouldUseFeatureControls(toOMVersion("4.4.0")))
	assert.True(t, ShouldUseFeatureControls(toOMVersion("4.4.1")))
	assert.True(t, ShouldUseFeatureControls(toOMVersion("5.0.1")))

	// Cloud Manager also supports it
	assert.True(t, ShouldUseFeatureControls(toOMVersion("v20020201")))

}

func toOMVersion(versionString string) versionutil.OpsManagerVersion {
	if versionString == "" {
		return versionutil.OpsManagerVersion{}
	}

	return versionutil.OpsManagerVersion{
		VersionString: fmt.Sprintf("%s.56729.20191105T2247Z", versionString),
	}
}
