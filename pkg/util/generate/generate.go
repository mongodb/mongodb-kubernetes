package generate

import (
	"crypto/rand"
	"encoding/base64"
	"unicode"
)

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

// RandomValidDNS1123Label generates a random fixed-length string with characters in a certain range.
func RandomValidDNS1123Label(n int) (string, error) {
	str, err := RandomFixedLengthStringOfSize(n)
	if err != nil {
		return "", err
	}

	runes := []rune(str)

	// Make sure that any letters are lowercase and that if any non-alphanumeric characters appear they are set to '0'.
	for i, r := range runes {
		if unicode.IsLetter(r) {
			runes[i] = unicode.ToLower(r)
		} else if !unicode.IsNumber(r) {
			runes[i] = rune('0')
		}
	}

	return string(runes), nil
}
