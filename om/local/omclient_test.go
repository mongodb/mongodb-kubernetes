package local

import (
	"fmt"
	"strconv"
	"testing"

	"github.com/10gen/ops-manager-kubernetes/om"
	"go.uber.org/zap"
)

// These are the tests for real environments - they must not be run automatically - only for manual checks.
// There is no easy way to exclude them individually so we have to move to internal package
func init() {
	logger, _ := zap.NewDevelopment()
	zap.ReplaceGlobals(logger)
}

var conn = om.OmConnection{
	BaseUrl:      "http://ec2-52-91-254-220.compute-1.amazonaws.com:8080",
	PublicApiKey: "aa51909d-b32b-488a-9104-74cd9f02e082",
	User:         "alisovenko@gmail.com",
	GroupId:      "5adc90c6e95d393033caf1f9"}

func TestRealStandalone(t *testing.T) {
	standalone := (om.NewMongodProcess("3.6.3")).SetHostName("ip-172-31-27-139.ec2.internal").SetName("merchantsStandalone").
		SetDbPath("/data").SetLogPath("/data/mongodb.log")

	deployment, err := conn.ReadDeployment()

	if err != nil {
		panic(err)
	}
	deployment.MergeStandalone(standalone)

	doUpdateDeployment(deployment)
}

func TestRealReplicaSet(t *testing.T) {
	d, err := conn.ReadDeployment()

	if err != nil {
		panic(err)
	}

	d.MergeReplicaSet(om.NewReplicaSetWithProcesses(om.NewReplicaSet("fooRs"), createReplicaSetProcesses("fooRs")))

	doUpdateDeployment(d)
}

func TestGenerateAgentKey(t *testing.T) {
	key, err := conn.GenerateAgentKey()

	if err != nil {
		panic(err)
	}

	fmt.Println(key)
}

func TestRealShardedCluster(t *testing.T) {
	d, err := conn.ReadDeployment()

	if err != nil {
		panic(err)
	}

	configRs := om.NewReplicaSetWithProcesses(om.NewReplicaSet("configSrv"), createReplicaSetProcesses("configSrv"))
	shards := createShards(3, "myShard")

	err = d.MergeShardedCluster("cluster", createMongosProcesses(3, "pretty"), configRs, shards)

	if err != nil {
		panic(err)
	}

	doUpdateDeployment(d)

	configRs = om.NewReplicaSetWithProcesses(om.NewReplicaSet("configSrv"), createReplicaSetProcesses("configSrv"))
	shards = createShards(4, "myShard")

	err = d.MergeShardedCluster("cluster", createMongosProcesses(4, "pretty"), configRs, shards)

	if err != nil {
		panic(err)
	}

	doUpdateDeployment(d)
}

func doUpdateDeployment(d *om.Deployment) {
	response, err := conn.UpdateDeployment(d)

	if err != nil {
		fmt.Println(err)
	}

	fmt.Println("-----------------------")

	fmt.Println(response)
}

func createReplicaSetProcesses(rsName string) []om.Process {
	rsMembers := make([]om.Process, 3)

	for i := 0; i < 3; i++ {
		idx := strconv.Itoa(i)
		rsMembers[i] = om.NewMongodProcess("3.6.3").SetHostName(rsName + idx + ".some.host").SetName(rsName + idx).
			SetDbPath("/data").SetLogPath("/data/mongodb.log")
		// We add replicaset member to check that replicaset name field was initialized during merge
	}
	return rsMembers
}

func createMongosProcesses(num int, name string) []om.Process {
	mongosProcesses := make([]om.Process, num)

	for i := 0; i < num; i++ {
		idx := strconv.Itoa(i)
		mongosProcesses[i] = om.NewMongosProcess("3.6.3").SetHostName("mongoS" + idx + ".some.host").SetName(name + idx)
	}
	return mongosProcesses
}

func createShards(number int, name string) []om.ReplicaSetWithProcesses {
	shards := make([]om.ReplicaSetWithProcesses, number)
	for i := 0; i < number; i++ {
		idx := strconv.Itoa(i)
		shards[i] = om.NewReplicaSetWithProcesses(om.NewReplicaSet(name+idx), createReplicaSetProcesses(name+idx))
	}
	return shards
}
