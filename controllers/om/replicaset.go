package om

import (
	"fmt"
	"sort"

	"github.com/spf13/cast"
	"go.uber.org/zap"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/mongodb-community-operator/pkg/automationconfig"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
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
	var protocolVersion string
	compare, err := util.CompareVersions(version, "3.2.0")
	if err != nil {
		zap.S().Warnf("Failed to parse version %s: %s", version, err)
	} else if compare >= 0 {
		protocolVersion = "1"
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
	// Practice shows that the type of unmarshalled data can be even float64 (no floating point though) or int32
	return cast.ToInt(r["_id"])
}

func (r ReplicaSetMember) Votes() int {
	return cast.ToInt(r["votes"])
}

func (r ReplicaSetMember) Priority() float32 {
	return cast.ToFloat32(r["priority"])
}

func (r ReplicaSetMember) Tags() map[string]string {
	return r["tags"].(map[string]string)
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
	return fmt.Sprintf("\"%s\" (members: %v)", r.Name(), r.Members())
}

// ***************************************** Private methods ***********************************************************

func initDefaultRs(set ReplicaSet, name string, protocolVersion string) {
	if protocolVersion != "" {
		// Automation Agent considers the cluster config with protocol version as string
		set["protocolVersion"] = protocolVersion
	}
	set.setName(name)
}

// Adding a member to the replicaset. The _id for the new member is calculated
// based on last existing member in the RS.
func (r ReplicaSet) addMember(process Process, id string, options automationconfig.MemberOptions) {
	members := r.Members()
	lastIndex := -1
	if len(members) > 0 {
		lastIndex = members[len(members)-1].Id()
	}

	rsMember := ReplicaSetMember{}
	rsMember["_id"] = id
	if id == "" {
		rsMember["_id"] = lastIndex + 1
	}
	rsMember["host"] = process.Name()

	// We always set this member to have vote (it will be set anyway on creation of deployment in OM), though this can
	// be overriden by OM during merge and corrected in the end (as rs can have only 7 voting members)
	rsMember.setVotes(options.GetVotes()).setPriority(options.GetPriority()).setTags(options.GetTags())
	r.setMembers(append(members, rsMember))
}

// mergeFrom merges "operatorRs" into "OM" one
func (r ReplicaSet) mergeFrom(operatorRs ReplicaSet) []string {
	initDefaultRs(r, operatorRs.Name(), operatorRs.protocolVersion())

	// technically we use "operatorMap" as the target map which will be used to update the members
	// for the 'r' object
	omMap := buildMapOfRsNodes(r)
	operatorMap := buildMapOfRsNodes(operatorRs)

	// merge overlapping members into the operatorMap (overriding the 'host',
	// 'horizons' and '_id' fields only)
	for k, currentValue := range omMap {
		if otherValue, ok := operatorMap[k]; ok {
			currentValue["host"] = otherValue.Name()
			currentValue["_id"] = otherValue.Id()
			currentValue["votes"] = otherValue.Votes()
			currentValue["priority"] = otherValue.Priority()
			currentValue["tags"] = otherValue.Tags()
			horizons := otherValue.getHorizonConfig()
			if len(horizons) > 0 {
				currentValue["horizons"] = horizons
			} else {
				delete(currentValue, "horizons")
			}
			operatorMap[k] = currentValue
		}
	}

	// find OM members that will be removed from RS. This can be either the result of scaling
	// down or just OM added some members on its own
	removedMembers := findDifference(omMap, operatorMap)

	// update replicaset back
	replicas := make([]ReplicaSetMember, len(operatorMap))
	i := 0
	for _, v := range operatorMap {
		replicas[i] = v
		i++
	}
	sort.Slice(replicas, func(i, j int) bool {
		return replicas[i].Id() < replicas[j].Id()
	})
	r.setMembers(replicas)

	return removedMembers
}

// members returns all members of replica set. Note, that this should stay package-private as 'operator' package should
// not have direct access to members.
// The members returned are not copies and can be used direcly for mutations
func (r ReplicaSet) Members() []ReplicaSetMember {
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
	members := r.Members()
	for _, m := range members {
		if m.Name() == name {
			return &m
		}
	}

	return nil
}

// mms uses string for this field to make it optional in json
func (r ReplicaSet) protocolVersion() string {
	return r["protocolVersion"].(string)
}

func (r ReplicaSetMember) getHorizonConfig() mdbv1.MongoDBHorizonConfig {
	if horizons, okay := r["horizons"]; okay {
		return horizons.(mdbv1.MongoDBHorizonConfig)
	}
	return mdbv1.MongoDBHorizonConfig{}
}

func (r ReplicaSetMember) setHorizonConfig(horizonConfig mdbv1.MongoDBHorizonConfig) ReplicaSetMember {
	// must not set empty horizon config
	if len(horizonConfig) > 0 {
		r["horizons"] = horizonConfig
	}

	return r
}

// Note, that setting vote to 0 without setting priority to the same value is not correct
func (r ReplicaSetMember) setVotes(votes int) ReplicaSetMember {
	r["votes"] = votes

	return r
}

func (r ReplicaSetMember) setPriority(priority float32) ReplicaSetMember {
	r["priority"] = priority

	return r
}

func (r ReplicaSetMember) setTags(tags map[string]string) ReplicaSetMember {
	finalTags := make(map[string]string)
	for k, v := range tags {
		finalTags[k] = v
	}
	r["tags"] = finalTags
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
	for _, r := range rs.Members() {
		ans[r.Name()] = r
	}
	return ans
}
