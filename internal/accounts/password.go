package accounts

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// The password hash is PBKDF2-HMAC-SHA256 from the standard library (Go 1.24
// and newer), so the project still has no dependency outside it. The
// iteration count follows OWASP's guidance for this construction. It is a
// variable only so that the tests can run fast.
var iterations = 600_000

const (
	hashScheme = "pbkdf2-sha256"
	saltLen    = 16
	keyLen     = 32

	// MinPasswordLen and MaxPasswordLen bound what an account may choose. The
	// maximum keeps a login request from becoming a hashing denial of service.
	MinPasswordLen = 12
	MaxPasswordLen = 128

	// minAcceptedIterations is the lowest count a stored hash may claim:
	// a damaged or forged file must not weaken the check.
	minAcceptedIterations = 1000
	maxAcceptedIterations = 10_000_000
)

// HashPassword returns the stored form "pbkdf2-sha256$iterations$salt$key".
func HashPassword(password string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	return hashWith(password, salt, iterations)
}

func hashWith(password string, salt []byte, iter int) (string, error) {
	key, err := pbkdf2.Key(sha256.New, password, salt, iter, keyLen)
	if err != nil {
		return "", err
	}
	enc := base64.RawStdEncoding
	return fmt.Sprintf("%s$%d$%s$%s", hashScheme, iter, enc.EncodeToString(salt), enc.EncodeToString(key)), nil
}

var errBadHash = errors.New("accounts: unsupported password hash")

// CheckPassword compares password with a stored hash in constant time.
func CheckPassword(password, stored string) (bool, error) {
	parts := strings.Split(stored, "$")
	if len(parts) != 4 || parts[0] != hashScheme {
		return false, errBadHash
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil || iter < minAcceptedIterations || iter > maxAcceptedIterations {
		return false, errBadHash
	}
	enc := base64.RawStdEncoding
	salt, err := enc.DecodeString(parts[2])
	if err != nil || len(salt) < 8 {
		return false, errBadHash
	}
	want, err := enc.DecodeString(parts[3])
	if err != nil || len(want) != keyLen {
		return false, errBadHash
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, iter, keyLen)
	if err != nil {
		return false, err
	}
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// PasswordProblem returns the key of what is wrong with a new password, or "".
func PasswordProblem(password, username string) string {
	switch {
	case len(password) < MinPasswordLen:
		return "password_short"
	case len(password) > MaxPasswordLen:
		return "password_long"
	case strings.EqualFold(password, username):
		return "password_username"
	case allSame(password):
		return "password_weak"
	}
	return ""
}

func allSame(s string) bool {
	for i := 1; i < len(s); i++ {
		if s[i] != s[0] {
			return false
		}
	}
	return true
}

// SetCost changes the PBKDF2 iteration count used for new hashes and returns
// a function that restores it. It exists for tests of other packages, where
// the production count would make every sign-in take a fifth of a second.
func SetCost(n int) (restore func()) {
	old := iterations
	iterations = n
	return func() { iterations = old }
}
