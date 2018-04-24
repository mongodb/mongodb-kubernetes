package local

import (
	"fmt"
	"strconv"
	"testing"

	"github.com/10gen/ops-manager-kubernetes/om"
)

// These are the tests for real environments - they must not be run automatically - only for manual checks.
// There is no easy way to exclude them individually so we have to move to internal package

var conn = om.OmConnection{
	BaseUrl:      "http://ec2-52-91-170-83.compute-1.amazonaws.com:8080",
	PublicApiKey: "bf5ed778-153f-4e85-8f0f-f6320338f7bf",
	User:         "alisovenko@gmail.com",
	GroupId:      "5aa1097f5030a75c8d886a3a"}

func TestRealStandalone(t *testing.T) {
	standalone := (om.NewProcess("3.6.3")).SetHostName("ip-172-31-27-139.ec2.internal").SetName("merchantsStandalone").
		SetDbPath("/data").SetLogPath("/data/mongodb.log")

	deployment, err := conn.ReadDeployment()

	if err != nil {
		panic(err)
	}
	deployment.MergeStandalone(standalone)

	response, err := conn.UpdateDeployment(deployment)

	if err != nil {
		fmt.Println(err)
	}

	fmt.Println("-----------------------")

	fmt.Println(response)
}

func TestRealReplicaSet(t *testing.T) {
	d, err := conn.ReadDeployment()

	if err != nil {
		panic(err)
	}

	d.MergeReplicaSet("fooRs", createReplicaSetProcesses())

	response, err := conn.UpdateDeployment(d)

	if err != nil {
		fmt.Println(err)
	}

	fmt.Println("-----------------------")

	fmt.Println(response)
}

func TestGenerateAgentKey(t *testing.T) {
	key, err := conn.GenerateAgentKey()

	if err != nil {
		panic(err)
	}

	fmt.Println(key)
}

func createReplicaSetProcesses() []om.Process {
	rsMembers := make([]om.Process, 3)

	for i := 0; i < 3; i++ {
		idx := strconv.Itoa(i)
		rsMembers[i] = om.NewProcess("3.6.3").SetHostName("mongo" + idx + ".some.host").SetName("merchantsStandalone" + idx).
			SetDbPath("/data").SetLogPath("/data/mongodb.log")
		// We add replicaset member to check that replicaset name field was initialized during merge
	}
	return rsMembers
}
