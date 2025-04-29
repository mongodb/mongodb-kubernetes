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
