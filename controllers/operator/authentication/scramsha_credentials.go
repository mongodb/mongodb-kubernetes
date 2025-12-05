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
	ExternalDB = "$external"

	clientKeyInput = "Client Key" // specified in RFC 5802
	serverKeyInput = "Server Key" // specified in RFC 5802

	// using the default MongoDB values for the number of iterations depending on mechanism
	scramSha1Iterations   = 10000
	scramSha256Iterations = 15000

	rfc5802MandatedSaltSize = 4
)

func isPasswordChanged(user *om.MongoDBUser, password string, acUser *om.MongoDBUser) (bool, error) {
	// if something unexpected happens return true to make sure that the scramShaCreds will be generated again.
	if acUser == nil || acUser.ScramSha256Creds == nil || acUser.ScramSha1Creds == nil {
		return true, nil
	}

	if acUser.ScramSha256Creds.Salt != "" && acUser.ScramSha1Creds.Salt != "" {
		sha256Salt, err := base64.StdEncoding.DecodeString(acUser.ScramSha256Creds.Salt)
		if err != nil {
			return true, err
		}
		sha1Salt, err := base64.StdEncoding.DecodeString(acUser.ScramSha1Creds.Salt)
		if err != nil {
			return true, nil
		}
		// generate scramshacreds with (new) password but with old salt to verify if given password
		// is actually changed
		newScramSha256Creds, err := computeScramShaCreds(user.Username, password, sha256Salt, ScramSha256)
		if err != nil {
			return false, xerrors.Errorf("error generating scramSah256 creds to verify with already present user on automation config %w", err)
		}

		newScramSha1Creds, err := computeScramShaCreds(user.Username, password, sha1Salt, MongoDBCR)
		if err != nil {
			return false, xerrors.Errorf("error generating scramSah256 creds to verify with already present user on automation config %w", err)
		}
		return !newScramSha256Creds.Equals(*acUser.ScramSha256Creds) || !newScramSha1Creds.Equals(*acUser.ScramSha1Creds), nil
	}

	return true, nil
}

// ConfigureScramCredentials creates both SCRAM-SHA-1 and SCRAM-SHA-256 credentials. This ensures
// that changes to the authentication settings on the MongoDB resources won't leave MongoDBUsers without
// the correct credentials.
func ConfigureScramCredentials(user *om.MongoDBUser, password string, ac *om.AutomationConfig) error {
	// there are chances that the reconciliation is happening again for this user resource and we wouldn't
	// want to generate scram creds again if the password of the user has not changed.
	_, acUser := ac.Auth.GetUser(user.Username, user.Database)
	changed, err := isPasswordChanged(user, password, acUser)
	if err != nil {
		return err
	}
	if !changed {
		// since the scram creds generated using the old salt are same with the scram creds stored in automation config
		// there is no need to generate new salt/creds.
		user.ScramSha256Creds = acUser.ScramSha256Creds
		user.ScramSha1Creds = acUser.ScramSha1Creds
		return nil
	}

	scram256Salt, err := generateSalt(sha256.New)
	if err != nil {
		return xerrors.Errorf("error generating scramSha256 salt: %w", err)
	}

	scram1Salt, err := generateSalt(sha1.New)
	if err != nil {
		return xerrors.Errorf("error generating scramSha1 salt: %w", err)
	}

	scram256Creds, err := computeScramShaCreds(user.Username, password, scram256Salt, ScramSha256)
	if err != nil {
		return xerrors.Errorf("error generating scramSha256 creds: %w", err)
	}
	scram1Creds, err := computeScramShaCreds(user.Username, password, scram1Salt, MongoDBCR)
	if err != nil {
		return xerrors.Errorf("error generating scramSha1Creds: %w", err)
	}
	user.ScramSha256Creds = scram256Creds
	user.ScramSha1Creds = scram1Creds
	return nil
}

// The code in this file is largely adapted from the Automation Agent codebase.
// https://github.com/10gen/mms-automation/blob/c108e0319cc05c0d8719ceea91a0424a016db583/go_planner/src/com.tengen/cm/crypto/scram.go

// computeScramShaCreds takes a plain text password and a specified mechanism name and generates
// the ScramShaCreds which will be embedded into a MongoDBUser.
func computeScramShaCreds(username, password string, salt []byte, name MechanismName) (*om.ScramShaCreds, error) {
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

// generateSalt will create a salt for use with computeScramShaCreds based on the given hashConstructor.
// sha1.New should be used for MONGODB-CR/SCRAM-SHA-1 and sha256.New should be used for SCRAM-SHA-256
func generateSalt(hashConstructor func() hash.Hash) ([]byte, error) {
	saltSize := hashConstructor().Size() - rfc5802MandatedSaltSize
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
	if len(salt) != hashSize-rfc5802MandatedSaltSize {
		return nil, xerrors.Errorf("salt should have a size of %v bytes, but instead has a size of %v bytes", hashSize-rfc5802MandatedSaltSize, len(salt))
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
