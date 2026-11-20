package passwordhash

import (
	"crypto/sha256"
	"encoding/base64"

	"golang.org/x/crypto/pbkdf2"

	"github.com/mongodb/mongodb-kubernetes/pkg/util/generate"
)

const (
	HashIterations = 256
	HashLength     = 32
)

// PasswordMatchesHash verifies whether the password reproduces the stored hash when
// hashed with the stored salt using the same PBKDF2 parameters.
func PasswordMatchesHash(password, storedHash, storedSalt string) (bool, error) {
	salt, err := base64.StdEncoding.DecodeString(storedSalt)
	if err != nil {
		return false, err
	}
	hash := pbkdf2.Key([]byte(password), salt, HashIterations, HashLength, sha256.New)
	computed := base64.StdEncoding.EncodeToString(hash)
	return computed == storedHash, nil
}

// GenerateHashAndSaltForPassword returns a `hash` and `salt` for this password.
func GenerateHashAndSaltForPassword(password string) (string, string) {
	salt, err := generate.GenerateRandomBytes(8)
	if err != nil {
		return "", ""
	}

	// The implementation at
	// https://github.com/10gen/mms-automation/blob/76078d46d56a91a7ca2edc91b811ee87682b24b6/go_planner/src/com.tengen/cm/metrics/prometheus/server.go#L207
	// is the counterpart of this code.
	hash := pbkdf2.Key([]byte(password), salt, HashIterations, HashLength, sha256.New)
	return base64.StdEncoding.EncodeToString(hash), base64.StdEncoding.EncodeToString(salt)
}
