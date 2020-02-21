package v1

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMongoDB_RunValidations_BadHorizonsMemberCount(t *testing.T) {
	replicaSetHorizons := []MongoDBHorizonConfig{
		{"my-horizon": "my-db.com:12345"},
		{"my-horizon": "my-db.com:12346"},
	}

	rs := NewReplicaSetBuilder().SetSecurityTLSEnabled().Build()
	rs.Spec.Connectivity = &MongoDBConnectivity{}
	rs.Spec.Connectivity.ReplicaSetHorizons = replicaSetHorizons
	err := rs.RunValidations()
	assert.Equal(t, "Number of horizons must be equal to number of members in replica set", err.Error())
}

func TestMongoDB_RunValidations_HorizonsWithoutTLS(t *testing.T) {
	replicaSetHorizons := []MongoDBHorizonConfig{
		{"my-horizon": "my-db.com:12345"},
		{"my-horizon": "my-db.com:12342"},
		{"my-horizon": "my-db.com:12346"},
	}

	rs := NewReplicaSetBuilder().Build()
	rs.Spec.Connectivity = &MongoDBConnectivity{}
	rs.Spec.Connectivity.ReplicaSetHorizons = replicaSetHorizons
	err := rs.RunValidations()
	assert.Equal(t, "TLS must be enabled in order to use replica set horizons", err.Error())
}

func TestMongoDB_RunValidations(t *testing.T) {
	rs := NewReplicaSetBuilder().Build()
	assert.Equal(t, rs.RunValidations(), nil)
}
