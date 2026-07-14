package scram

import (
	"crypto/sha1" // nolint
	"crypto/sha256"
	"hash"

	"github.com/mongodb/mongodb-kubernetes/pkg/authentication/scramcredentials"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/generate"
)

// Salts generates 2 different salts. The first is for the sha1 algorithm,
// the second is for sha256.
func Salts() ([]byte, []byte, error) {
	sha1Salt, err := scramSalt(sha1.New)
	if err != nil {
		return nil, nil, err
	}

	sha256Salt, err := scramSalt(sha256.New)
	if err != nil {
		return nil, nil, err
	}
	return sha1Salt, sha256Salt, nil
}

// scramSalt creates a salt which can be used to compute Scram Sha credentials based on the given hashConstructor.
// sha1.New should be used for MONGODB-CR/SCRAM-SHA-1 and sha256.New should be used for SCRAM-SHA-256.
func scramSalt(hashConstructor func() hash.Hash) ([]byte, error) {
	saltSize := hashConstructor().Size() - scramcredentials.RFC5802MandatedSaltSize
	s, err := generate.RandomFixedLengthStringOfSize(20)
	if err != nil {
		return nil, err
	}
	shaBytes32 := sha256.Sum256([]byte(s))

	// the algorithms expect a salt of a specific size.
	return shaBytes32[:saltSize], nil
}
