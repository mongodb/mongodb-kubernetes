package test

type MemberClusterDetails struct {
	ClusterName           string
	ShardMap              []int
	NumberOfConfigServers int
	NumberOfMongoses      int
}

type MemberClusters struct {
	ClusterNames             []string
	ShardDistribution        []map[string]int
	ConfigServerDistribution map[string]int
	MongosDistribution       map[string]int
}

func NewMemberClusters(memberClusterDetails ...MemberClusterDetails) MemberClusters {
	ret := MemberClusters{
		ClusterNames:             make([]string, 0),
		ShardDistribution:        make([]map[string]int, 0),
		ConfigServerDistribution: make(map[string]int),
		MongosDistribution:       make(map[string]int),
	}
	for _, detail := range memberClusterDetails {
		ret.ClusterNames = append(ret.ClusterNames, detail.ClusterName)
		ret.ConfigServerDistribution[detail.ClusterName] = detail.NumberOfConfigServers
		ret.MongosDistribution[detail.ClusterName] = detail.NumberOfMongoses
	}

	// TODO: Think if shards map shouldn't be an argument of this method? We're always using 0 index?
	for range memberClusterDetails[0].ShardMap {
		shardDistribution := map[string]int{}
		for clusterDetailIndex, clusterDetails := range memberClusterDetails {
			shardDistribution[clusterDetails.ClusterName] = memberClusterDetails[0].ShardMap[clusterDetailIndex]
		}
		ret.ShardDistribution = append(ret.ShardDistribution, shardDistribution)
	}

	return ret
}

func (clusters MemberClusters) ShardCount() int {
	ignoredValue := 1
	var distinctShardNumbers map[int]int = make(map[int]int)
	for _, shardDistribution := range clusters.ShardDistribution {
		for _, shardNumber := range shardDistribution {
			distinctShardNumbers[shardNumber] = ignoredValue
		}
	}
	return len(distinctShardNumbers)
}
