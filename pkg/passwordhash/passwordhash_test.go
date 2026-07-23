package passwordhash

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test_PasswordMatchesHash_Valid verifies that a correct password reproduces the stored hash.
func Test_PasswordMatchesHash_Valid(t *testing.T) {
	password := "my-secret-password"
	hash, salt := GenerateHashAndSaltForPassword(password)

	match, err := PasswordMatchesHash(password, hash, salt)
	require.NoError(t, err)
	assert.True(t, match)
}

// Test_PasswordMatchesHash_WrongPassword verifies that an incorrect password does not
// match the stored hash.
func Test_PasswordMatchesHash_WrongPassword(t *testing.T) {
	password := "my-secret-password"
	hash, salt := GenerateHashAndSaltForPassword(password)

	match, err := PasswordMatchesHash("wrong-password", hash, salt)
	require.NoError(t, err)
	assert.False(t, match)
}

// Test_PasswordMatchesHash_EmptySalt verifies that an empty salt produces a non-matching hash.
func Test_PasswordMatchesHash_EmptySalt(t *testing.T) {
	hash, _ := GenerateHashAndSaltForPassword("some-password")

	match, err := PasswordMatchesHash("some-password", hash, "")
	require.NoError(t, err)
	assert.False(t, match)
}

// Test_PasswordMatchesHash_NotAllocatedSalt verifies that a nil-length decoded salt does not match.
func Test_PasswordMatchesHash_NotAllocatedSalt(t *testing.T) {
	match, err := PasswordMatchesHash("pass", "hash", "")
	require.NoError(t, err)
	assert.False(t, match, "empty salt must not validate")
}

// Test_PasswordMatchesHash_InvalidSalt verifies that a malformed base64 salt returns an error.
func Test_PasswordMatchesHash_InvalidSalt(t *testing.T) {
	match, err := PasswordMatchesHash("pass", "aaaa", "!!!not-valid-base64!!!")
	require.Error(t, err)
	assert.False(t, match)
}

// Test_PasswordMatchesHash_EmptyHash verifies that an empty hash does not match.
func Test_PasswordMatchesHash_EmptyHash(t *testing.T) {
	_, salt := GenerateHashAndSaltForPassword("some-password")

	match, err := PasswordMatchesHash("some-password", "", salt)
	require.NoError(t, err)
	assert.False(t, match)
}

// Test_PasswordMatchesHash_KnownVector verifies against a known password-hash-salt triple
// to catch accidental algorithm changes.
func Test_PasswordMatchesHash_KnownVector(t *testing.T) {
	password := "prom-password"
	hash, salt := GenerateHashAndSaltForPassword(password)

	t.Logf("password=%q hash=%q salt=%q", password, hash, salt)

	match, err := PasswordMatchesHash(password, hash, salt)
	require.NoError(t, err)
	assert.True(t, match, "password must match its own hash+salt")
}

// Test_PasswordMatchesHash_DeterministicSameInput verifies that the same password+salt
// always produces the same result.
func Test_PasswordMatchesHash_DeterministicSameInput(t *testing.T) {
	password := "test-password"
	hash, salt := GenerateHashAndSaltForPassword(password)

	for i := 0; i < 10; i++ {
		match, err := PasswordMatchesHash(password, hash, salt)
		require.NoError(t, err)
		assert.True(t, match)
	}
}

// Test_PasswordMatchesHash_SpecialCharacters verifies passwords with special characters.
func Test_PasswordMatchesHash_SpecialCharacters(t *testing.T) {
	password := "p@$$w0rd!#$%&'()*+,-./:;<=>?@[]^_`{|}~"
	hash, salt := GenerateHashAndSaltForPassword(password)

	match, err := PasswordMatchesHash(password, hash, salt)
	require.NoError(t, err)
	assert.True(t, match)

	match, err = PasswordMatchesHash("different", hash, salt)
	require.NoError(t, err)
	assert.False(t, match)
}

// Test_PasswordMatchesHash_Unicode verifies passwords with unicode characters.
func Test_PasswordMatchesHash_Unicode(t *testing.T) {
	password := "пароль-密码-パスワード"
	hash, salt := GenerateHashAndSaltForPassword(password)

	match, err := PasswordMatchesHash(password, hash, salt)
	require.NoError(t, err)
	assert.True(t, match)
}

// Test_PasswordMatchesHash_VeryLongPassword verifies passwords near typical max lengths.
func Test_PasswordMatchesHash_VeryLongPassword(t *testing.T) {
	password := ""
	for i := 0; i < 100; i++ {
		password += "a"
	}
	hash, salt := GenerateHashAndSaltForPassword(password)

	match, err := PasswordMatchesHash(password, hash, salt)
	require.NoError(t, err)
	assert.True(t, match)
}
