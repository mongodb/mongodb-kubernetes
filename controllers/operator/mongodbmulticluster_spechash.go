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
// The spec is canonicalized before hashing: under an object key, null, empty object, empty array
// and absent are one equivalence class. API-server round-trips are not byte-neutral (observed: a
// nil additionalMongodConfig comes back as {}), so without this an in-memory spec and its own
// stored form would hash differently. The rule for consumers stays: hash the object as READ from
// your API server. The full operator-binary skew contract (which fields, which versions) is
// TODO 5 in the spike notes — this is only its POC face.
func multiClusterSpecHash(spec mdbmultiv1.MongoDBMultiSpec) (string, error) {
	// json.Marshal sorts map keys, so the serialization is deterministic; Spec.Mapping is
	// excluded via `json:"-"`, so operator-internal mutation never perturbs the hash
	jsonBytes, err := json.Marshal(spec)
	if err != nil {
		return "", xerrors.Errorf("could not marshal the MongoDBMultiCluster spec to JSON: %w", err)
	}

	generic := map[string]interface{}{}
	if err := json.Unmarshal(jsonBytes, &generic); err != nil {
		return "", xerrors.Errorf("could not canonicalize the MongoDBMultiCluster spec: %w", err)
	}
	canonical, _ := canonicalizeJSONValue(generic)

	canonicalBytes, err := json.Marshal(canonical)
	if err != nil {
		return "", xerrors.Errorf("could not marshal the canonicalized MongoDBMultiCluster spec: %w", err)
	}
	hashBytes := sha256.Sum256(canonicalBytes)
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(hashBytes[:]), nil
}

// canonicalizeJSONValue prunes representational noise in place: object keys holding null, an
// empty object or an empty array are removed (one equivalence class with "absent"). Array
// elements are never dropped — positions are content — only canonicalized within. Scalars are
// always content, including "", 0 and false. Returns the canonical value and whether it is empty
// from its parent's point of view.
func canonicalizeJSONValue(v interface{}) (interface{}, bool) {
	switch t := v.(type) {
	case nil:
		return nil, true
	case map[string]interface{}:
		for k, val := range t {
			canonical, empty := canonicalizeJSONValue(val)
			if empty {
				delete(t, k)
			} else {
				t[k] = canonical
			}
		}
		return t, len(t) == 0
	case []interface{}:
		for i, val := range t {
			canonical, _ := canonicalizeJSONValue(val)
			t[i] = canonical
		}
		return t, len(t) == 0
	default:
		return t, false
	}
}
