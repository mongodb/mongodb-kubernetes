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
	//deployment.mergeStandalone(standalone)

	deployment, err := conn.ReadDeployment()

	//deployment, err := ReadDeployment("http://localhost:8080", "5a97ee01423de74ad13c3a3a",
	//	"alisovenko@gmail.com", "74ca5b58-7a58-4f2b-bbbb-b396005bd7b8")

	if err != nil {
		panic(err)
	}
	deployment.MergeStandalone(standalone)

	response, err := conn.ApplyDeployment(deployment)
	//response, err := ApplyDeployment("http://localhost:8080", "5a97ee01423de74ad13c3a3a",
	//	deployment, "alisovenko@gmail.com", "74ca5b58-7a58-4f2b-bbbb-b396005bd7b8")

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

	response, err := conn.ApplyDeployment(d)
	//response, err := ApplyDeployment("http://localhost:8080", "5a97ee01423de74ad13c3a3a",
	//	deployment, "alisovenko@gmail.com", "74ca5b58-7a58-4f2b-bbbb-b396005bd7b8")

	if (err != nil) {
		fmt.Println(err)
	}

	fmt.Println("-----------------------")

	fmt.Println(response)
}

