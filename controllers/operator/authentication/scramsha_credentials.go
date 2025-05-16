package authentication

import (
	"crypto/hmac"
	"crypto/sha1" //nolint //Part of the algorithm
	"crypto/sha256"
	"encoding/base64"
	"hash"

	"github.com/xdg/stringprep"
	"golang.org/x/xerrors"

	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/generate"
)

const (
	clientKeyInput = "Client Key" // specified in RFC 5802
	serverKeyInput = "Server Key" // specified in RFC 5802

	// using the default MongoDB values for the number of iterations depending on mechanism
	scramSha1Iterations   = 10000
	scramSha256Iterations = 15000

	RFC5802MandatedSaltSize = 4
)

// The code in this file is largely adapted from the Automation Agent codebase.
// https://github.com/10gen/mms-automation/blob/c108e0319cc05c0d8719ceea91a0424a016db583/go_planner/src/com.tengen/cm/crypto/scram.go

// ComputeScramShaCreds takes a plain text password and a specified mechanism name and generates
// the ScramShaCreds which will be embedded into a MongoDBUser.
func ComputeScramShaCreds(username, password string, salt []byte, name MechanismName) (*om.ScramShaCreds, error) {
	var hashConstructor func() hash.Hash
	iterations := 0
	if name == ScramSha256 {
		hashConstructor = sha256.New
		iterations = scramSha256Iterations
	} else if name == MongoDBCR {
		hashConstructor = sha1.New
		iterations = scramSha1Iterations

		// MONGODB-CR/SCRAM-SHA-1 requires the hash of the password being passed computeScramCredentials
		// instead of the plain text password. Generated the same was that Ops Manager does.
		// See: https://github.com/10gen/mms/blob/a941f11a81fba4f85a9890eaf27605bd344af2a8/server/src/main/com/xgen/svc/mms/deployment/auth/AuthUser.java#L290
		password = util.MD5Hex(username + ":mongo:" + password)
	} else {
		return nil, xerrors.Errorf("unrecognized SCRAM-SHA format %s", name)
	}
	base64EncodedSalt := base64.StdEncoding.EncodeToString(salt)
	return computeScramCredentials(hashConstructor, iterations, base64EncodedSalt, password)
}

// GenerateSalt will create a salt for use with ComputeScramShaCreds based on the given hashConstructor.
// sha1.New should be used for MONGODB-CR/SCRAM-SHA-1 and sha256.New should be used for SCRAM-SHA-256
func GenerateSalt(hashConstructor func() hash.Hash) ([]byte, error) {
	saltSize := hashConstructor().Size() - RFC5802MandatedSaltSize
	salt, err := generate.RandomFixedLengthStringOfSize(saltSize)
	if err != nil {
		return nil, err
	}
	return []byte(salt), nil
}

func generateSaltedPassword(hashConstructor func() hash.Hash, password string, salt []byte, iterationCount int) ([]byte, error) {
	preparedPassword, err := stringprep.SASLprep.Prepare(password)
	if err != nil {
		return nil, xerrors.Errorf("error SASLprep'ing password: %w", err)
	}

	result, err := hmacIteration(hashConstructor, []byte(preparedPassword), salt, iterationCount)
	if err != nil {
		return nil, xerrors.Errorf("error running hmacIteration: %w", err)
	}
	return result, nil
}

func hmacIteration(hashConstructor func() hash.Hash, input, salt []byte, iterationCount int) ([]byte, error) {
	hashSize := hashConstructor().Size()

	// incorrect salt size will pass validation, but the credentials will be invalid. i.e. it will not
	// be possible to auth with the password provided to create the credentials.
	if len(salt) != hashSize-RFC5802MandatedSaltSize {
		return nil, xerrors.Errorf("salt should have a size of %v bytes, but instead has a size of %v bytes", hashSize-RFC5802MandatedSaltSize, len(salt))
	}

	startKey := append(salt, 0, 0, 0, 1)
	result := make([]byte, hashSize)

	hmacHash := hmac.New(hashConstructor, input)
	if _, err := hmacHash.Write(startKey); err != nil {
		return nil, xerrors.Errorf("error running hmacHash: %w", err)
	}

	intermediateDigest := hmacHash.Sum(nil)

	copy(result, intermediateDigest)

	for i := 1; i < iterationCount; i++ {
		hmacHash.Reset()
		if _, err := hmacHash.Write(intermediateDigest); err != nil {
			return nil, xerrors.Errorf("error running hmacHash: %w", err)
		}

		intermediateDigest = hmacHash.Sum(nil)

		for i := 0; i < len(intermediateDigest); i++ {
			result[i] ^= intermediateDigest[i]
		}
	}

	return result, nil
}

func generateClientOrServerKey(hashConstructor func() hash.Hash, saltedPassword []byte, input string) ([]byte, error) {
	hmacHash := hmac.New(hashConstructor, saltedPassword)
	if _, err := hmacHash.Write([]byte(input)); err != nil {
		return nil, xerrors.Errorf("error running hmacHash: %w", err)
	}

	return hmacHash.Sum(nil), nil
}

func generateStoredKey(hashConstructor func() hash.Hash, clientKey []byte) ([]byte, error) {
	h := hashConstructor()
	if _, err := h.Write(clientKey); err != nil {
		return nil, xerrors.Errorf("error hashing: %w", err)
	}
	return h.Sum(nil), nil
}

func generateSecrets(hashConstructor func() hash.Hash, password string, salt []byte, iterationCount int) (storedKey, serverKey []byte, err error) {
	saltedPassword, err := generateSaltedPassword(hashConstructor, password, salt, iterationCount)
	if err != nil {
		return nil, nil, xerrors.Errorf("error generating salted password: %w", err)
	}

	clientKey, err := generateClientOrServerKey(hashConstructor, saltedPassword, clientKeyInput)
	if err != nil {
		return nil, nil, xerrors.Errorf("error generating client key: %w", err)
	}

	storedKey, err = generateStoredKey(hashConstructor, clientKey)
	if err != nil {
		return nil, nil, xerrors.Errorf("error generating stored key: %w", err)
	}

	serverKey, err = generateClientOrServerKey(hashConstructor, saltedPassword, serverKeyInput)
	if err != nil {
		return nil, nil, xerrors.Errorf("error generating server key: %w", err)
	}

	return storedKey, serverKey, err
}

func generateB64EncodedSecrets(hashConstructor func() hash.Hash, password, b64EncodedSalt string, iterationCount int) (storedKey, serverKey string, err error) {
	salt, err := base64.StdEncoding.DecodeString(b64EncodedSalt)
	if err != nil {
		return "", "", xerrors.Errorf("error decoding salt: %w", err)
	}

	unencodedStoredKey, unencodedServerKey, err := generateSecrets(hashConstructor, password, salt, iterationCount)
	if err != nil {
		return "", "", xerrors.Errorf("error generating secrets: %w", err)
	}

	storedKey = base64.StdEncoding.EncodeToString(unencodedStoredKey)
	serverKey = base64.StdEncoding.EncodeToString(unencodedServerKey)
	return storedKey, serverKey, nil
}

// password should be encrypted in the case of SCRAM-SHA-1 and unencrypted in the case of SCRAM-SHA-256
func computeScramCredentials(hashConstructor func() hash.Hash, iterationCount int, base64EncodedSalt string, password string) (*om.ScramShaCreds, error) {
	storedKey, serverKey, err := generateB64EncodedSecrets(hashConstructor, password, base64EncodedSalt, iterationCount)
	if err != nil {
		return nil, xerrors.Errorf("error generating SCRAM-SHA keys: %w", err)
	}

	return &om.ScramShaCreds{IterationCount: iterationCount, Salt: base64EncodedSalt, StoredKey: storedKey, ServerKey: serverKey}, nil
}
