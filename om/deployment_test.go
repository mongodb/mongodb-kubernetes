package om

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestSerialize(t *testing.T) {
	deployment := newDeployment("3.6.3")
	standalone := (NewStandalone("3.6.3")).HostPort("mongo1.some.host").Name("merchantsStandalone").
		DbPath("/data/mongodb").LogPath("/data/mongodb/mongodb.log")
	deployment.MergeStandalone(standalone)

	data, _ := json.Marshal(deployment)
	// todo check against serialized content
	fmt.Printf("%s", string(data))
}

// First time merge adds the new standalone
// second invocation doesn't add new node as the existing standalone is found (by name) and the data is merged
func TestMergeStandalone(t *testing.T) {
	deployment := newDeployment("3.6.3")
	standalone := (NewStandalone("3.6.3")).HostPort("mongo1.some.host").Name("merchantsStandalone").
		DbPath("/data/mongodb").LogPath("/data/mongodb/mongodb.log")
	deployment.MergeStandalone(standalone)

	if len(deployment.Processes) != 1 {
		t.Error("One process is expected to be added after the first merge")
	}

	deployment.Version = 5
	deployment.Processes[0].Alias = "alias"
	deployment.Processes[0].Hostname = "foo"

	deployment.MergeStandalone(standalone)

	if len(deployment.Processes) != 1 {
		t.Error("Second merge is expected to update existing process")
	}
	if deployment.Processes[0].Hostname != "mongo1.some.host" {
		fmt.Print(deployment.Processes[0].Hostname)
		t.Error("Merge is expected to update the Hostname back")
	}
	if deployment.Processes[0].Alias != "alias" || deployment.Version != 5 {
		t.Error("Merge is not expected to update other fields (like alias or version)")
	}

}
