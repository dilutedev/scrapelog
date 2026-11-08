package apikeys

import (
	"regexp"
	"strings"
	"testing"
)

var generatedKeyPattern = regexp.MustCompile(`^sl_[0-9A-Za-z]{12}_[0-9A-Za-z]{43}$`)

func TestGenerateProducesParseableKeys(t *testing.T) {
	keyID, fullKey, err := Generate()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !generatedKeyPattern.MatchString(fullKey) {
		t.Fatalf("key %q does not match the expected format", fullKey)
	}
	parsedKeyID, ok := ParseKeyID(fullKey)
	if !ok || parsedKeyID != keyID {
		t.Fatalf("ParseKeyID returned %q, %v; want %q", parsedKeyID, ok, keyID)
	}
	_, otherKey, err := Generate()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if otherKey == fullKey {
		t.Fatal("two generated keys are equal")
	}
}

func TestParseKeyIDRejectsMalformedKeys(t *testing.T) {
	identifier := strings.Repeat("a", 12)
	secret := strings.Repeat("b", 43)
	malformedKeys := map[string]string{
		"empty":            "",
		"missing prefix":   identifier + "_" + secret,
		"wrong prefix":     "xx_" + identifier + "_" + secret,
		"two parts":        "sl_" + identifier + secret,
		"four parts":       "sl_" + identifier + "_" + secret + "_c",
		"short id":         "sl_" + identifier[:11] + "_" + secret,
		"long id":          "sl_" + identifier + "a_" + secret,
		"short secret":     "sl_" + identifier + "_" + secret[:42],
		"non base62 chars": "sl_" + identifier + "_" + secret[:42] + "-",
	}
	for name, malformedKey := range malformedKeys {
		if _, ok := ParseKeyID(malformedKey); ok {
			t.Errorf("%s: %q was accepted", name, malformedKey)
		}
	}
}

func TestHashReturnsSha256Hex(t *testing.T) {
	expected := "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
	if got := Hash("test"); got != expected {
		t.Fatalf("got %s, want %s", got, expected)
	}
}
