package main

import (
	"fmt"
	"testing"
)

func TestFindVersion(t *testing.T) {
	clusterConfig := testClusterConfiguration()

	processVersion := getProcessVersionForStandalone("test-process-name", clusterConfig)
	if processVersion != "3.6.3" {
		t.Error(fmt.Sprintf("Did not find 3.6.3 process version, instead found '%s'", processVersion))
	}

	processVersion = getProcessVersionForStandalone("test-process-name-another", clusterConfig)
	if processVersion != "3.4.8" {
		t.Error(fmt.Sprintf("Did not find 3.4.8 process version, instead found '%s'", processVersion))
	}

	processVersion = getProcessVersionForStandalone("non-existent-process", clusterConfig)
	if processVersion != "" {
		t.Error(fmt.Sprintf("Did not find empty (\"\") process version, instead found '%s'", processVersion))
	}
}
