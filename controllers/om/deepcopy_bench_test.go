package om

// Reviewer-only benchmarks for PR #1696. Not intended to merge.
//
//	go test ./controllers/om/ -run '^$' -bench 'IsEqualAfterModification|DeepCopy' -benchmem -count 3

import (
	"bytes"
	"encoding/gob"
	"encoding/json"
	"testing"

	"github.com/mitchellh/copystructure"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/equality"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/maputil"
)

// mapDeepCopyGob is master's util.MapDeepCopy.
func mapDeepCopyGob(m map[string]any) (map[string]any, error) {
	gob.Register(map[string]any{})
	gob.Register([]any{})

	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(m); err != nil {
		return nil, err
	}
	var cp map[string]any
	if err := gob.NewDecoder(&buf).Decode(&cp); err != nil {
		return nil, err
	}
	return cp, nil
}

// isEqualAfterModificationGob is master's isEqualAfterModification.
func isEqualAfterModificationGob(changeDeploymentFunc func(Deployment) error, deployment Deployment) (bool, error) {
	original, err := mapDeepCopyGob(deployment)
	if err != nil {
		return false, err
	}
	if err := changeDeploymentFunc(deployment); err != nil {
		return false, err
	}
	deploymentWithoutTypes, err := maputil.StructToMap(deployment)
	if err != nil {
		return false, err
	}
	return equality.Semantic.DeepEqual(deploymentWithoutTypes, original), nil
}

// benchDeployment is a 2-replica-set deployment as decoded from Ops Manager JSON.
func benchDeployment(b *testing.B) Deployment {
	d := getTestAutomationConfig().Deployment
	d.addReplicaSet(NewReplicaSet("rs-1", "3.2.0"))
	d.addReplicaSet(NewReplicaSet("rs-2", "3.2.0"))
	raw, err := json.Marshal(d)
	require.NoError(b, err)
	require.NoError(b, json.Unmarshal(raw, &d))
	return d
}

type isEqualFunc func(func(Deployment) error, Deployment) (bool, error)

var isEqualImpls = map[string]isEqualFunc{"gob": isEqualAfterModificationGob, "json": isEqualAfterModification}

func BenchmarkIsEqualAfterModification(b *testing.B) {
	b.Run("no-op", func(b *testing.B) {
		deployment := benchDeployment(b)
		for name, compare := range isEqualImpls {
			b.Run(name, func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					if _, err := compare(func(Deployment) error { return nil }, deployment); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	})

	b.Run("struct-valued-write", func(b *testing.B) {
		role := mdbv1.MongoDBRole{
			Role: "inherited", Db: "admin", Privileges: []mdbv1.Privilege{},
			Roles: []mdbv1.InheritedRole{{Role: "read", Db: "admin"}},
		}
		wire, err := maputil.StructToMap(Deployment{"roles": []mdbv1.MongoDBRole{role}})
		require.NoError(b, err)
		deployment := benchDeployment(b)
		setRoles := func(d Deployment) error {
			d.SetRoles([]mdbv1.MongoDBRole{role})
			return nil
		}
		for name, compare := range isEqualImpls {
			b.Run(name, func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					// restore the wire form so every iteration crosses the map-to-struct boundary
					deployment["roles"] = wire["roles"]
					equal, err := compare(setRoles, deployment)
					if err != nil || !equal {
						b.Fatalf("equal=%v err=%v", equal, err)
					}
				}
			})
		}
	})
}

func BenchmarkDeepCopy(b *testing.B) {
	inputs := map[string]map[string]any{
		"mongod-config": {
			"net":          map[string]any{"port": 27017, "tls": map[string]any{"mode": "requireTLS"}},
			"storage":      map[string]any{"engine": "wiredTiger", "dbPath": "/data"},
			"setParameter": map[string]any{"a": true},
		},
		"deployment": benchDeployment(b),
	}
	for input, m := range inputs {
		b.Run(input+"/gob", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := mapDeepCopyGob(m); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(input+"/copystructure", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := copystructure.Copy(m); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
