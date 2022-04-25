package generate

import (
	"crypto/rand"
	"encoding/base64"
	mrand "math/rand"
	"time"
)

var letters = []rune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ123")

// final key must be between 6 and at most 1024 characters
func KeyFileContents() (string, error) {
	return generateRandomString(500)
}

func RandomFixedLengthStringOfSize(n int) (string, error) {
	b, err := GenerateRandomBytes(n)
	return base64.URLEncoding.EncodeToString(b)[:n], err
}

func GenerateRandomBytes(size int) ([]byte, error) {
	b := make([]byte, size)
	_, err := rand.Read(b)
	if err != nil {
		return nil, err
	}

	return b, nil
}

func generateRandomString(numBytes int) (string, error) {
	b, err := GenerateRandomBytes(numBytes)
	return base64.StdEncoding.EncodeToString(b), err
}

func randSeq(n int) string {
	b := make([]rune, n)
	for i := range b {
		b[i] = letters[mrand.Intn(len(letters))]
	}
	return string(b)
}

func GenerateRandomPassword() string {
	mrand.Seed(time.Now().UnixNano())
	return randSeq(10)
}
