package om

import "sort"

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

func NewReplicaSet(name string) ReplicaSet {
	ans := ReplicaSet{}
	ans["_id"] = name
	ans["members"] = make([]ReplicaSetMember, 0)
	return ans
}

func (r ReplicaSet) Name() string {
	return r["_id"].(string)
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
func (r ReplicaSet) MergeFrom(otherRs ReplicaSet) []string {
	// technically we use "otherMap" as the target map which will be used to update the members for the 'r' object
	currentMap := buildMapOfRsNodes(r)
	otherMap := buildMapOfRsNodes(otherRs)

	// merge overlapping members to the otherMap (overriding 'host' field and then )
	for k, currentValue := range currentMap {
		if otherValue, ok := otherMap[k]; ok {
			currentValue["host"] = otherValue["host"]
			otherMap[k] = currentValue
		}
	}

	// find OM members that will be removed from RS. This can be either the result of scaling down or just OM added
	// some members on its own
	removedMembers := findDifference(currentMap, otherMap)

	// update replicaset back
	replicas := make([]ReplicaSetMember, len(otherMap))
	i := 0
	for _, v := range otherMap {
		replicas[i] = v
		i++
	}
	sort.Slice(replicas, func(i, j int) bool {
		return replicas[i]["_id"].(int) < replicas[j]["_id"].(int)
	})
	r.setMembers(replicas)

	return removedMembers
}

/*func (r ReplicaSet) String() string {
	return fmt.Sprintf("\"%s\" (%d members)", r.Name(), len(r.members()))
}*/

// ***************************************** Private methods ***********************************************************

// Adding a member to the replicaset. The _id for the new member is calculated based on last existing member in the RS.
// Note that any other configuration (arbiterOnly/priority etc) can be passed as the argument to the function if needed
func (r ReplicaSet) addMember(process Process) {
	members := r.members()
	lastIndex := -1
	if len(members) > 0 {
		lastIndex = members[len(members)-1]["_id"].(int)
	}

	rsMember := ReplicaSetMember{}
	rsMember["_id"] = lastIndex + 1
	rsMember["host"] = process.Name()
	r.setMembers(append(members, rsMember))
}

func (r ReplicaSet) members() []ReplicaSetMember {
	switch v := r["members"].(type) {
	case []ReplicaSetMember:
		return v
	case [] interface{}:
		ans := make([]ReplicaSetMember, len(v))
		for i, val := range v {
			ans[i] = NewReplicaSetMemberFromInterface(val)
		}
		return ans
	default:
		panic("Unexpected type of members variable")
	}
}

func (r ReplicaSet) setMembers(members []ReplicaSetMember) {
	r["members"] = members
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
		ans[r["host"].(string)] = r
	}
	return ans
}
