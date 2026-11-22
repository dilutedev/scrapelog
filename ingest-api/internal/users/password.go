package users

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
)

var passwordHashIterations = 600_000

const passwordHashPrefix = "pbkdf2-sha256"

const dummyPasswordHash = "pbkdf2-sha256$600000$CNUIWf1dDKGjrr13ekl/3Q$s9vLg7unNQf3BcOBnfSz82a4Y0HgfRHCL2klYn6ttzE"

func HashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	derivedKey, err := pbkdf2.Key(sha256.New, password, salt, passwordHashIterations, 32)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s$%d$%s$%s", passwordHashPrefix, passwordHashIterations,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(derivedKey)), nil
}

func passwordMatches(storedHash string, password string) bool {
	parts := strings.Split(storedHash, "$")
	if len(parts) != 4 || parts[0] != passwordHashPrefix {
		return false
	}
	iterations, err := strconv.Atoi(parts[1])
	if err != nil || iterations <= 0 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	expectedKey, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil || len(expectedKey) == 0 {
		return false
	}
	derivedKey, err := pbkdf2.Key(sha256.New, password, salt, iterations, len(expectedKey))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(derivedKey, expectedKey) == 1
}
