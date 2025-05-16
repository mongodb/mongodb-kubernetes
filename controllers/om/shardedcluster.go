package om

import (
	"sort"

	"github.com/mongodb/mongodb-kubernetes/pkg/util/stringutil"
)

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
		s := newShard(v.Rs.Name())
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
func (s ShardedCluster) mergeFrom(operatorCluster ShardedCluster) []string {
	s.setName(operatorCluster.Name())
	s.setConfigServerRsName(operatorCluster.ConfigServerRsName())

	omMap := buildMapOfShards(s)
	operatorMap := buildMapOfShards(operatorCluster)

	// merge overlapping members to the operatorMap
	for k, currentValue := range omMap {
		if otherValue, ok := operatorMap[k]; ok {
			currentValue.mergeFrom(otherValue)

			operatorMap[k] = currentValue
		}
	}

	// find OM shards that will be removed from cluster. This can be either the result of shard cluster reconfiguration
	// or just OM added some shards on its own
	removedMembers := findDifferentKeys(omMap, operatorMap)

	// update cluster shards back
	shards := make([]Shard, len(operatorMap))
	i := 0
	for _, v := range operatorMap {
		shards[i] = v
		i++
	}
	sort.Slice(shards, func(i, j int) bool {
		return shards[i].id() < shards[j].id()
	})
	s.setShards(shards)

	return removedMembers
}

// mergeFrom merges the operator shard into OM one. Only some fields are overriden, the others stay untouched
func (s Shard) mergeFrom(operatorShard Shard) {
	s.setId(operatorShard.id())
	s.setRs(operatorShard.rs())
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

// draining returns the "draining" array which contains the names of replicasets for shards which are currently being
// removed. This is necessary for the AutomationAgent to keep the knowledge about shards to survive restarts (it's
// necessary to restart the mongod with the same 'shardSrv' option even if the shard is not in sharded cluster in
// Automation Config anymore)
func (s ShardedCluster) draining() []string {
	if _, ok := s["draining"]; !ok {
		return make([]string, 0)
	}

	// When go unmarhals an empty list from Json, it becomes
	// []interface{} and not []string, so we must check for
	// that particular case.
	if obj, ok := s["draining"].([]interface{}); ok {
		var hostNames []string
		for _, hn := range obj {
			hostNames = append(hostNames, hn.(string))
		}

		return hostNames
	}

	return s["draining"].([]string)
}

func (s ShardedCluster) setDraining(rsNames []string) {
	s["draining"] = rsNames
}

func (s ShardedCluster) addToDraining(rsNames []string) {
	// constructor is a better place to initialize the array, but we aim a better backward compatibility with OM 4.0
	// versions (which learnt about this field in 4.0.12) so doing lazy initialization
	if _, ok := s["draining"]; !ok {
		s.setDraining([]string{})
	}
	for _, r := range rsNames {
		if !stringutil.Contains(s.draining(), r) {
			s["draining"] = append(s.draining(), r)
		}
	}
}

func (s ShardedCluster) removeDraining() {
	delete(s, "draining")
}

// getAllReplicaSets returns all replica sets associated with sharded cluster
func (s ShardedCluster) getAllReplicaSets() []string {
	var ans []string
	for _, s := range s.shards() {
		ans = append(ans, s.rs())
	}
	ans = append(ans, s.ConfigServerRsName())
	return ans
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
