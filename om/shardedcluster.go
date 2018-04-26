package om

import "sort"

// Representation of one element in "sharding" array in OM json deployment:
/*
"sharding": [
            {
                "shards": [
                    {
                        "tags": [
                            "gluetenfree"
                        ],
                        "_id": "electron_0",
                        "rs": "electron_0"
                    },
                    {
                        "tags": [],
                        "_id": "electron_1",
                        "rs": "electron_1"
                    }
                ],
                "name": "electron",
                "managedSharding": true,
                "configServer": [],
                "configServerReplica": "electroncsrs",
                "collections": [
                    {
                        "_id": "food.vegetables",
                        "dropped": false,
                        "key": [
                            [
                                "rand",
                                1
                            ]
                        ],
                        "unique": false
                    },
                    {
                        "_id": "randomStuff.bigStuff",
                        "dropped": false,
                        "key": [
                            [
                                "x",
                                "hashed"
                            ]
                        ],
                        "unique": false
                    }
                ],
                "tags": []
            }
        ]
    }
*/
type ShardedCluster map[string]interface{}

type Shard map[string]interface{}

func NewShardedClusterFromInterface(i interface{}) ShardedCluster {
	return i.(map[string]interface{})
}

// NewShardedCluster builds a shard configuration with shards by replicasets names
func NewShardedCluster(name, configRsName string, replicaSets []ReplicaSetWithProcesses) ShardedCluster {
	ans := ShardedCluster{}
	ans.setName(name)
	ans.setConfigServerRsName(configRsName)

	shards := make([]Shard, len(replicaSets))
	for k, v := range replicaSets {
		s := newShard(v.rs.Name())
		shards[k] = s
	}
	ans.setShards(shards)
	return ans
}

func (s ShardedCluster) Name() string {
	return s["name"].(string)
}

func (s ShardedCluster) ConfigServerRsName() string {
	return s["configServerReplica"].(string)
}

// ***************************************** Private methods ***********************************************************

func newShard(name string) Shard {
	s := Shard{}
	s.setId(name)
	s.setRs(name)
	return s
}

// mergeFrom merges the other (Kuberenetes owned) cluster configuration into OM one
func (s ShardedCluster) mergeFrom(otherCluster ShardedCluster) []string {
	s.setName(otherCluster.Name())
	s.setConfigServerRsName(otherCluster.ConfigServerRsName())

	currentMap := buildMapOfShards(s)
	otherMap := buildMapOfShards(otherCluster)

	// merge overlapping members to the otherMap (overriding '_id' and 'rs" fields only)
	for k, currentValue := range currentMap {
		if otherValue, ok := otherMap[k]; ok {
			currentValue.setId(otherValue.id())
			currentValue.setRs(otherValue.rs())

			otherMap[k] = currentValue
		}
	}

	// find OM shards that will be removed from cluster. This can be either the result of shard cluster reconfiguration
	// or just OM added some shards on its own
	removedMembers := findDifferentKeys(currentMap, otherMap)

	// update cluster shards back
	shards := make([]Shard, len(otherMap))
	i := 0
	for _, v := range otherMap {
		shards[i] = v
		i++
	}
	sort.Slice(shards, func(i, j int) bool {
		return shards[i].id() < shards[j].id()
	})
	s.setShards(shards)

	return removedMembers

}

func (s ShardedCluster) shards() []Shard {
	switch v := s["shards"].(type) {
	case []Shard:
		return v
	case []interface{}:
		ans := make([]Shard, len(v))
		for i, val := range v {
			ans[i] = val.(map[string]interface{})
		}
		return ans
	default:
		panic("Unexpected type of shards variable")
	}
}

func (s ShardedCluster) setConfigServerRsName(name string) {
	s["configServerReplica"] = name
}

func (s ShardedCluster) setName(name string) {
	s["name"] = name
}

func (s ShardedCluster) setShards(shards []Shard) {
	s["shards"] = shards
}

func (s Shard) id() string {
	return s["_id"].(string)
}

func (s Shard) setId(id string) {
	s["_id"] = id
}

func (s Shard) rs() string {
	return s["rs"].(string)
}

func (s Shard) setRs(rsName string) {
	s["rs"] = rsName
}

// Returns keys that exist in leftMap but don't exist in right one
func findDifferentKeys(leftMap map[string]Shard, rightMap map[string]Shard) []string {
	ans := make([]string, 0)
	for k := range leftMap {
		if _, ok := rightMap[k]; !ok {
			ans = append(ans, k)
		}
	}
	return ans
}

// Builds the map[<shard name>]<shard>. This makes intersection easier
func buildMapOfShards(sh ShardedCluster) map[string]Shard {
	ans := make(map[string]Shard)
	for _, r := range sh.shards() {
		ans[r.id()] = r
	}
	return ans
}
