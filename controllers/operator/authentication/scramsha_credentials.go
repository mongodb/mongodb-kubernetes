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

// isCredChanged returns true when the password does not reproduce the stored creds,
// or when creds are nil/empty (treated as missing).
func isCredChanged(username, password string, creds *om.ScramShaCreds, mechanism MechanismName) (bool, error) {
	if creds == nil || creds.Salt == "" {
		return true, nil
	}
	salt, err := base64.StdEncoding.DecodeString(creds.Salt)
	if err != nil {
		return true, xerrors.Errorf("error decoding salt for user %s: %w", username, err)
	}
	derived, err := computeScramShaCreds(username, password, salt, mechanism)
	if err != nil {
		return false, xerrors.Errorf("error deriving creds to compare for user %s: %w", username, err)
	}
	return !derived.Equals(*creds), nil
}

// hasCreds reports whether stored creds are complete enough to validate a password against.
func hasCreds(creds *om.ScramShaCreds) bool {
	return creds != nil && creds.Salt != ""
}

// PasswordMatchesStoredCreds reports whether the password reproduces every complete set
// of SCRAM credentials stored on the AC user. Incomplete creds are skipped. It returns
// an error when the user is missing or has no creds to validate against.
func PasswordMatchesStoredCreds(username, password string, acUser *om.MongoDBUser) (bool, error) {
	if acUser == nil {
		return false, xerrors.Errorf("user %s not found in the automation config", username)
	}
	validated := false
	if hasCreds(acUser.ScramSha256Creds) {
		changed, err := isCredChanged(username, password, acUser.ScramSha256Creds, ScramSha256)
		if err != nil {
			return false, err
		}
		if changed {
			return false, nil
		}
		validated = true
	}
	if hasCreds(acUser.ScramSha1Creds) {
		changed, err := isCredChanged(username, password, acUser.ScramSha1Creds, MongoDBCR)
		if err != nil {
			return false, err
		}
		if changed {
			return false, nil
		}
		validated = true
	}
	if !validated {
		return false, xerrors.Errorf("user %s has no SCRAM credentials to validate the supplied password against", username)
	}
	return true, nil
}

// ConfigureScramCredentials sets SCRAM credentials on user and returns needsFollowUp.
// Three cases:
//  1. User not in AC: generate both SHA-256 and SHA-1 creds.
//  2. Mechanisms set in AC: preserve matching creds, error on mismatch, needsFollowUp=true.
//     InitPassword is written so OM generates creds for its active mechanisms. The follow-up
//     reconcile appends the remaining algorithm via the no-mechanisms path (case 3).
//  3. No mechanisms in AC: preserve or regenerate each algorithm independently on password change.
func ConfigureScramCredentials(user *om.MongoDBUser, password string, ac *om.AutomationConfig) (bool, error) {
	_, acUser := ac.Auth.GetUser(user.Username, user.Database)

	if acUser == nil {
		var err error
		user.ScramSha256Creds, err = newScramSha256Creds(user.Username, password)
		if err != nil {
			return false, err
		}
		user.ScramSha1Creds, err = newScramSha1Creds(user.Username, password)
		if err != nil {
			return false, err
		}
		return false, nil
	}

	sha256Changed, err := isCredChanged(user.Username, password, acUser.ScramSha256Creds, ScramSha256)
	if err != nil {
		return false, err
	}
	sha1Changed, err := isCredChanged(user.Username, password, acUser.ScramSha1Creds, MongoDBCR)
	if err != nil {
		return false, err
	}

	if len(acUser.Mechanisms) > 0 {
		// Incomplete creds (missing or empty salt) cannot validate a password,
		// so they are treated as absent rather than as a mismatch.
		sha256Present := hasCreds(acUser.ScramSha256Creds)
		sha1Present := hasCreds(acUser.ScramSha1Creds)
		if !sha256Present && !sha1Present {
			return false, xerrors.Errorf("user %s has mechanisms set in the automation config but no SCRAM credentials to validate the supplied password against", user.Username)
		}
		// Mechanisms set in AC: reject a mismatched password to avoid silently regenerating creds.
		if sha256Changed && sha256Present {
			return false, xerrors.Errorf("supplied password does not match existing scramSha256 credentials for user %s", user.Username)
		}
		if sha1Changed && sha1Present {
			return false, xerrors.Errorf("supplied password does not match existing scramSha1 credentials for user %s", user.Username)
		}
		// Preserve only the algorithms already present.
		if !sha256Changed {
			user.ScramSha256Creds = acUser.ScramSha256Creds
		}
		if !sha1Changed {
			user.ScramSha1Creds = acUser.ScramSha1Creds
		}
		// Null mechanisms: OM owns that field and the operator cannot write it directly.
		// On the next reconcile acUser.Mechanisms will be empty, entering case 3 which
		// fills in whichever algorithm is still missing.
		user.Mechanisms = nil
		user.InitPassword = password
		return true, nil
	}

	// No mechanisms in AC: preserve each algorithm independently; only regenerate the one
	// that is missing or whose stored creds no longer match the current password.
	if !sha256Changed {
		user.ScramSha256Creds = acUser.ScramSha256Creds
	} else {
		user.ScramSha256Creds, err = newScramSha256Creds(user.Username, password)
		if err != nil {
			return false, err
		}
	}
	if !sha1Changed {
		user.ScramSha1Creds = acUser.ScramSha1Creds
	} else {
		user.ScramSha1Creds, err = newScramSha1Creds(user.Username, password)
		if err != nil {
			return false, err
		}
	}
	return false, nil
}

func newScramSha256Creds(username, password string) (*om.ScramShaCreds, error) {
	salt, err := generateSalt(sha256.New)
	if err != nil {
		return nil, xerrors.Errorf("error generating scramSha256 salt: %w", err)
	}
	return computeScramShaCreds(username, password, salt, ScramSha256)
}

func newScramSha1Creds(username, password string) (*om.ScramShaCreds, error) {
	salt, err := generateSalt(sha1.New)
	if err != nil {
		return nil, xerrors.Errorf("error generating scramSha1 salt: %w", err)
	}
	return computeScramShaCreds(username, password, salt, MongoDBCR)
}

// The code in this file is largely adapted from the Automation Agent codebase.
// https://github.com/10gen/mms-automation/blob/c108e0319cc05c0d8719ceea91a0424a016db583/go_planner/src/com.tengen/cm/crypto/scram.go

// computeScramShaCreds takes a plain text password and a specified mechanism name and generates
// the ScramShaCreds which will be embedded into a MongoDBUser.
func computeScramShaCreds(username, password string, salt []byte, name MechanismName) (*om.ScramShaCreds, error) {
	var hashConstructor func() hash.Hash
	iterations := 0
	switch name {
	case ScramSha256:
		hashConstructor = sha256.New
		iterations = scramSha256Iterations
	case MongoDBCR:
		hashConstructor = sha1.New
		iterations = scramSha1Iterations

		// MONGODB-CR/SCRAM-SHA-1 requires the hash of the password being passed computeScramCredentials
		// instead of the plain text password. Generated the same was that Ops Manager does.
		// See: https://github.com/10gen/mms/blob/a941f11a81fba4f85a9890eaf27605bd344af2a8/server/src/main/com/xgen/svc/mms/deployment/auth/AuthUser.java#L290
		password = util.MD5Hex(username + ":mongo:" + password)
	default:
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
