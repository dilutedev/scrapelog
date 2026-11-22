package users

import (
	"os"
	"testing"
)

func TestMain(testMain *testing.M) {
	passwordHashIterations = 1000
	os.Exit(testMain.Run())
}

func TestPasswordRoundTrip(test *testing.T) {
	hash, err := HashPassword("correct horse battery")
	if err != nil {
		test.Fatalf("HashPassword: %v", err)
	}
	if !passwordMatches(hash, "correct horse battery") {
		test.Fatal("correct password did not match")
	}
	if passwordMatches(hash, "correct horse batterY") {
		test.Fatal("wrong password matched")
	}
}

func TestPasswordHashesDifferBySalt(test *testing.T) {
	first, _ := HashPassword("same password value")
	second, _ := HashPassword("same password value")
	if first == second {
		test.Fatal("two hashes of one password are identical")
	}
}

func TestPasswordMatchesRejectsMalformedHashes(test *testing.T) {
	cases := map[string]string{
		"empty":            "",
		"wrong prefix":     "bcrypt$1000$c2FsdA$aGFzaA",
		"too few parts":    "pbkdf2-sha256$1000$c2FsdA",
		"too many parts":   "pbkdf2-sha256$1000$c2FsdA$aGFzaA$extra",
		"zero iterations":  "pbkdf2-sha256$0$c2FsdA$aGFzaA",
		"text iterations":  "pbkdf2-sha256$many$c2FsdA$aGFzaA",
		"bad salt base64":  "pbkdf2-sha256$1000$!!!$aGFzaA",
		"bad hash base64":  "pbkdf2-sha256$1000$c2FsdA$!!!",
		"empty hash field": "pbkdf2-sha256$1000$c2FsdA$",
	}
	for name, storedHash := range cases {
		test.Run(name, func(test *testing.T) {
			if passwordMatches(storedHash, "anything") {
				test.Fatalf("malformed hash %q matched", storedHash)
			}
		})
	}
}
