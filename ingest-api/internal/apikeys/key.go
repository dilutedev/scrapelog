package apikeys

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

const (
	keyPrefix         = "sl_"
	keyIDRandomLength = 12
	secretLength      = 43
	base62Alphabet    = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	unbiasedByteLimit = 248
)

func randomBase62(length int) (string, error) {
	collected := make([]byte, 0, length)
	randomBytes := make([]byte, length)
	for len(collected) < length {
		if _, err := rand.Read(randomBytes); err != nil {
			return "", err
		}
		for _, randomByte := range randomBytes {
			if randomByte < unbiasedByteLimit && len(collected) < length {
				collected = append(collected, base62Alphabet[randomByte%62])
			}
		}
	}
	return string(collected), nil
}

func Generate() (keyID string, fullKey string, err error) {
	idRandom, err := randomBase62(keyIDRandomLength)
	if err != nil {
		return "", "", err
	}
	secret, err := randomBase62(secretLength)
	if err != nil {
		return "", "", err
	}
	keyID = keyPrefix + idRandom
	return keyID, keyID + "_" + secret, nil
}

func isBase62(text string, expectedLength int) bool {
	if len(text) != expectedLength {
		return false
	}
	for index := 0; index < len(text); index++ {
		if !strings.ContainsRune(base62Alphabet, rune(text[index])) {
			return false
		}
	}
	return true
}

func ParseKeyID(fullKey string) (string, bool) {
	parts := strings.Split(fullKey, "_")
	if len(parts) != 3 || parts[0] != "sl" {
		return "", false
	}
	if !isBase62(parts[1], keyIDRandomLength) || !isBase62(parts[2], secretLength) {
		return "", false
	}
	return keyPrefix + parts[1], true
}

func Hash(fullKey string) string {
	sum := sha256.Sum256([]byte(fullKey))
	return hex.EncodeToString(sum[:])
}
