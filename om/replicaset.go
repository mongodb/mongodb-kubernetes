package om

import (
	"fmt"
	"sort"

	"github.com/10gen/ops-manager-kubernetes/util"
	"github.com/spf13/cast"
)

/* This corresponds to:
 {
		"_id": "blue",
		"members": [
			{
				"_id": 0,
				"host": "blue_0"
			},
			{
				"_id": 1,
				"host": "blue_1"
			},
			{
				"_id": 2,
				"arbiterOnly": true,
				"host": "blue_2",
				"priority": 0
			}
		]
}*/
type ReplicaSet map[string]interface{}

/* This corresponds to:
 {
		"_id": 0,
		"host": "blue_0",
 		"priority": 0,
 		"slaveDelay": 0
 }*/
type ReplicaSetMember map[string]interface{}

func NewReplicaSetFromInterface(i interface{}) ReplicaSet {
	return i.(map[string]interface{})
}

func NewReplicaSetMemberFromInterface(i interface{}) ReplicaSetMember {
	return i.(map[string]interface{})
}

func NewReplicaSet(name, version string) ReplicaSet {
	ans := ReplicaSet{}
	ans["members"] = make([]ReplicaSetMember, 0)

	// "protocolVersion" was a new field in 3.2+ Mongodb
	var protocolVersion *int32
	v, e := util.ParseMongodbMinorVersion(version)
	if e == nil && v >= float32(3.2) {
		protocolVersion = util.Int32Ref(1)
	}

	initDefaultRs(ans, name, protocolVersion)

	return ans
}

func (r ReplicaSet) Name() string {
	return r["_id"].(string)
}

func (r ReplicaSetMember) Name() string {
	return r["host"].(string)
}

func (r ReplicaSetMember) Id() int {
	// Practice shows that the type of unmarshalled data can be even float64 (no floating point though) or int32..
	return cast.ToInt(r["_id"])
}

/* Merges the other replica set to the current one. "otherRs" members have higher priority (as they are supposed
 to be RS members managed by Kubernetes).
 Returns the list of names of members which were removed as the result of merge (either they were added by mistake in OM
 or we are scaling down)

 Example:

 Current RS:

 "members": [
		{
			"_id": 0,
			"host": "blue_0",
			"arbiterOnly": true
		},
		{
			"_id": 1,
			"host": "blue_1"
		}]

 Other RS:

 "members": [
		{
			"_id": 0,
			"host": "green_0"
		},
		{
			"_id": 2,
			"host": "green_2"
		}]

 Merge result:

 "members": [
		{
			"_id": 0,
			"host": "green_0",
			"arbiterOnly": true
		},
		{
			"_id": 2,
			"host": "green_2"
		}]
},*/

func (r ReplicaSet) String() string {
	return fmt.Sprintf("\"%s\" (members: %v)", r.Name(), r.members())
}

func (r ReplicaSetMember) String() string {
	return fmt.Sprintf("[id: %v, host: %v]", r.Name(), r.Id())
}

// ***************************************** Private methods ***********************************************************

func initDefaultRs(set ReplicaSet, name string, protocolVersion *int32) {
	if protocolVersion != nil {
		set["protocolVersion"] = protocolVersion
	}
	set.setName(name)
}

// Adding a member to the replicaset. The _id for the new member is calculated based on last existing member in the RS.
// Note that any other configuration (arbiterOnly/priority etc) can be passed as the argument to the function if needed
func (r ReplicaSet) addMember(process Process) {
	members := r.members()
	lastIndex := -1
	if len(members) > 0 {
		lastIndex = members[len(members)-1].Id()
	}

	rsMember := ReplicaSetMember{}
	rsMember["_id"] = lastIndex + 1
	rsMember["host"] = process.Name()
	r.setMembers(append(members, rsMember))
}

// mergeFrom merges "operator" "otherRs" into "OM" one
func (r ReplicaSet) mergeFrom(otherRs ReplicaSet) []string {
	initDefaultRs(r, otherRs.Name(), otherRs.protocolVersion())

	// technically we use "otherMap" as the target map which will be used to update the members
	// for the 'r' object
	currentMap := buildMapOfRsNodes(r)
	otherMap := buildMapOfRsNodes(otherRs)

	// merge overlapping members to the otherMap (overriding 'host' and '_id" fields)
	for k, currentValue := range currentMap {
		if otherValue, ok := otherMap[k]; ok {
			currentValue["host"] = otherValue.Name()
			currentValue["_id"] = otherValue.Id()
			otherMap[k] = currentValue
		}
	}

	// find OM members that will be removed from RS. This can be either the result of scaling
	// down or just OM added some members on its own
	removedMembers := findDifference(currentMap, otherMap)

	// update replicaset back
	replicas := make([]ReplicaSetMember, len(otherMap))
	i := 0
	for _, v := range otherMap {
		replicas[i] = v
		i++
	}
	sort.Slice(replicas, func(i, j int) bool {
		return replicas[i].Id() < replicas[j].Id()
	})
	r.setMembers(replicas)

	return removedMembers
}

func (r ReplicaSet) members() []ReplicaSetMember {
	switch v := r["members"].(type) {
	case []ReplicaSetMember:
		return v
	case []interface{}:
		ans := make([]ReplicaSetMember, len(v))
		for i, val := range v {
			ans[i] = NewReplicaSetMemberFromInterface(val)
		}
		return ans
	default:
		panic("Unexpected type of members variable")
	}
}

func (r ReplicaSet) setName(name string) {
	r["_id"] = name
}

func (r ReplicaSet) setMembers(members []ReplicaSetMember) {
	r["members"] = members
}

func (r ReplicaSet) clearMembers() {
	r["members"] = make([]ReplicaSetMember, 0)
}

func (r ReplicaSet) findMemberByName(name string) *ReplicaSetMember {
	members := r.members()
	for _, m := range members {
		if m.Name() == name {
			return &m
		}
	}

	return nil
}

func (r ReplicaSet) protocolVersion() *int32 {
	return r["protocolVersion"].(*int32)
}

func (r ReplicaSetMember) setVotes(votes int) ReplicaSetMember {
	r["votes"] = votes

	return r
}

func (r ReplicaSetMember) setPriority(priority int) ReplicaSetMember {
	r["priority"] = priority

	return r
}

// Returns keys that exist in leftMap but don't exist in right one
func findDifference(leftMap map[string]ReplicaSetMember, rightMap map[string]ReplicaSetMember) []string {
	ans := make([]string, 0)
	for k := range leftMap {
		if _, ok := rightMap[k]; !ok {
			ans = append(ans, k)
		}
	}
	return ans
}

// Builds the map[<process name>]<replica set member>. This makes intersection easier
func buildMapOfRsNodes(rs ReplicaSet) map[string]ReplicaSetMember {
	ans := make(map[string]ReplicaSetMember)
	for _, r := range rs.members() {
		ans[r.Name()] = r
	}
	return ans
}
