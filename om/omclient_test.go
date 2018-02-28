package om

import (
	"testing"
	"fmt"
)

func TestReal(t *testing.T) {
	deployment := newDeployment("3.6.3")
	standalone := (NewStandalone("3.6.3")).HostPort("ip-172-31-27-139.ec2.internal").Name("merchantsStandalone").
		DbPath("/data").LogPath("/data/mongodb.log")
	deployment.mergeStandalone(standalone)

	response, err := ApplyDeployment("http://ec2-184-73-133-183.compute-1.amazonaws.com:8080", "5a9411cf1aeca45c674a27cf",
		deployment, "alisovenko@gmail.com", "cb989e41-2804-4642-ae93-8e00004e3007")
	//response, err := ApplyDeployment("http://localhost:8080", "5a95d1ed327e415757c6592a",
	//	deployment, "alisovenko@gmail.com", "bdfd84c5-5518-4c26-b25d-8e89201e0ad1")

	if (err != nil) {
		fmt.Println(err)
	}

	fmt.Println("-----------------------")

	fmt.Println(response)
}
