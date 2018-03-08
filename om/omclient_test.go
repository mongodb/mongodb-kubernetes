package om

import (
	"testing"
	"fmt"
)

var conn = OmConnection{
	BaseUrl:      "http://ec2-52-91-170-83.compute-1.amazonaws.com:8080",
	PublicApiKey: "bf5ed778-153f-4e85-8f0f-f6320338f7bf",
	User:         "alisovenko@gmail.com",
	GroupId:      "5aa1097f5030a75c8d886a3a"}

func TestRealStandalone(t *testing.T) {
	standalone := (NewProcess("3.6.3")).SetHostName("ip-172-31-27-139.ec2.internal").SetName("merchantsStandalone").
		SetDbPath("/data").SetLogPath("/data/mongodb.log")

	deployment, err := conn.ReadDeployment()

	if err != nil {
		panic(err)
	}
	deployment.MergeStandalone(standalone)

	response, err := conn.UpdateDeployment(deployment)

	if (err != nil) {
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

	if (err != nil) {
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
