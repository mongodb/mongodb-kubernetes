package operator

import (
	"crypto/sha256"
	"encoding/base32"
	"encoding/json"

	"golang.org/x/xerrors"

	mdbmultiv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdbmulti"
)

// multiClusterSpecHash returns a deterministic hash of the MongoDBMultiCluster spec. In the
// decentralized topology the CR is replicated to every cluster by GitOps, so its
// metadata.generation is meaningless across copies — this hash is the only cross-cluster-stable
// spec version. The leader stamps it into every directive it writes (targetSpecHash) and members
// fence on equality with the hash of their own local copy.
//
// TODO(decentralized-poc): no canonicalization yet — adding a spec field with a defaulted value
// changes the hash across operator versions. Deferred to the materializer milestone.
func multiClusterSpecHash(spec mdbmultiv1.MongoDBMultiSpec) (string, error) {
	// json.Marshal sorts map keys, so the serialization is deterministic; Spec.Mapping is
	// excluded via `json:"-"`, so operator-internal mutation never perturbs the hash
	jsonBytes, err := json.Marshal(spec)
	if err != nil {
		return "", xerrors.Errorf("could not marshal the MongoDBMultiCluster spec to JSON: %w", err)
	}
	hashBytes := sha256.Sum256(jsonBytes)
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(hashBytes[:]), nil
}
