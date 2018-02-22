package om

import (
	"testing"
	"fmt"
	"encoding/json"
)

func TestMergeStandalone(t *testing.T) {
	deployment := newDeployment("3.6.3")
	standalone := (NewStandalone("3.6.3")).HostPort("mongo1.some.host").Name("merchantsStandalone").
		DbPath("/data/mongodb").LogPath("/data/mongodb/mongodb.log")
	deployment.mergeStandalone(standalone)

	data, _ := json.Marshal(deployment)
	fmt.Printf("%s", string(data))
}
