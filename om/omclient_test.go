package om

import (
	"testing"
	"fmt"
)

func TestRealStandalone(t *testing.T) {
	standalone := (NewProcess("3.6.3")).SetHostName("ip-172-31-27-139.ec2.internal").SetName("merchantsStandalone").
		SetDbPath("/data").SetLogPath("/data/mongodb.log")
	//deployment.mergeStandalone(standalone)

	deployment, err := ReadDeployment("http://ec2-54-84-71-167.compute-1.amazonaws.com:8080", "5a9d2397e0db835c92b47f70",
		"alisovenko@gmail.com", "3d85dd01-3f7b-48fe-85ac-3ad0071ec0d3")

	//deployment, err := ReadDeployment("http://localhost:8080", "5a97ee01423de74ad13c3a3a",
	//	"alisovenko@gmail.com", "74ca5b58-7a58-4f2b-bbbb-b396005bd7b8")

	if err != nil {
		panic(err)
	}
	deployment.MergeStandalone(standalone)

	response, err := ApplyDeployment("http://ec2-54-84-71-167.compute-1.amazonaws.com:8080", "5a9d2397e0db835c92b47f70",
		deployment, "alisovenko@gmail.com", "3d85dd01-3f7b-48fe-85ac-3ad0071ec0d3")
	//response, err := ApplyDeployment("http://localhost:8080", "5a97ee01423de74ad13c3a3a",
	//	deployment, "alisovenko@gmail.com", "74ca5b58-7a58-4f2b-bbbb-b396005bd7b8")

	if (err != nil) {
		fmt.Println(err)
	}

	fmt.Println("-----------------------")

	fmt.Println(response)
}

func TestRealReplicaSet(t *testing.T) {
	d, err := ReadDeployment("http://ec2-54-84-71-167.compute-1.amazonaws.com:8080", "5a9d2397e0db835c92b47f70",
		"alisovenko@gmail.com", "3d85dd01-3f7b-48fe-85ac-3ad0071ec0d3")

	if err != nil {
		panic(err)
	}

	d.MergeReplicaSet("fooRs", createReplicaSetProcesses())

	response, err := ApplyDeployment("http://ec2-54-84-71-167.compute-1.amazonaws.com:8080", "5a9d2397e0db835c92b47f70",
		d, "alisovenko@gmail.com", "3d85dd01-3f7b-48fe-85ac-3ad0071ec0d3")
	//response, err := ApplyDeployment("http://localhost:8080", "5a97ee01423de74ad13c3a3a",
	//	deployment, "alisovenko@gmail.com", "74ca5b58-7a58-4f2b-bbbb-b396005bd7b8")

	if (err != nil) {
		fmt.Println(err)
	}

	fmt.Println("-----------------------")

	fmt.Println(response)
}

