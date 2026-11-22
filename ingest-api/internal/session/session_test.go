package session

import (
	"encoding/base64"
	"strconv"
	"strings"
	"testing"
	"time"
)

var baseTime = time.Unix(1_700_000_000, 0)

func clockAt(currentTime *time.Time) func() time.Time {
	return func() time.Time { return *currentTime }
}

func TestVerifyReturnsIssuedClaims(t *testing.T) {
	currentTime := baseTime
	signer := New("admin-token", clockAt(&currentTime))
	claims, valid := signer.Verify(signer.Issue("user-123", 7))
	if !valid || claims.Subject != "user-123" || claims.SessionVersion != 7 {
		t.Fatalf("Verify = %+v, %v", claims, valid)
	}
}

func TestVerifyAcceptsIssuedValueUntilExpiry(t *testing.T) {
	tests := map[string]struct {
		advance time.Duration
		valid   bool
	}{
		"just issued":        {advance: 0, valid: true},
		"just before expiry": {advance: Lifetime - time.Second, valid: true},
		"at expiry":          {advance: Lifetime, valid: false},
		"after expiry":       {advance: Lifetime + time.Hour, valid: false},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			currentTime := baseTime
			signer := New("admin-token", clockAt(&currentTime))
			value := signer.Issue("admin", 0)
			currentTime = currentTime.Add(test.advance)
			if _, got := signer.Verify(value); got != test.valid {
				t.Fatalf("Verify = %v, want %v", got, test.valid)
			}
		})
	}
}

func TestVerifyRejectsTamperedAndForeignValues(t *testing.T) {
	currentTime := baseTime
	signer := New("admin-token", clockAt(&currentTime))
	issued := signer.Issue("user-123", 1)
	parts := strings.Split(issued, ".")
	subject, version, expiry, macText := parts[0], parts[1], parts[2], parts[3]
	extendedExpiry := strconv.FormatInt(baseTime.Add(2*Lifetime).Unix(), 10)
	flippedMAC := "A" + macText[1:]
	if flippedMAC == macText {
		flippedMAC = "B" + macText[1:]
	}
	otherSigner := New("other-token", clockAt(&currentTime))
	oldFormatMAC := base64.RawURLEncoding.EncodeToString(signer.mac(expiry))
	nonNumericVersionSigned := subject + ".x." + expiry
	nonNumericVersion := nonNumericVersionSigned + "." + base64.RawURLEncoding.EncodeToString(signer.mac(nonNumericVersionSigned))
	emptySubjectSigned := "." + version + "." + expiry
	emptySubject := emptySubjectSigned + "." + base64.RawURLEncoding.EncodeToString(signer.mac(emptySubjectSigned))

	tests := map[string]string{
		"tampered subject":    "user-456." + version + "." + expiry + "." + macText,
		"tampered version":    subject + ".2." + expiry + "." + macText,
		"tampered expiry":     subject + "." + version + "." + extendedExpiry + "." + macText,
		"tampered mac":        subject + "." + version + "." + expiry + "." + flippedMAC,
		"other token":         otherSigner.Issue("user-123", 1),
		"empty":               "",
		"no dot":              "nodothere",
		"two parts":           expiry + "." + oldFormatMAC,
		"three parts":         subject + "." + expiry + "." + macText,
		"five parts":          subject + "." + version + "." + expiry + ".extra." + macText,
		"empty subject":       emptySubject,
		"non numeric version": nonNumericVersion,
		"bad base64":          subject + "." + version + "." + expiry + ".!!!not-base64!!!",
	}
	for name, value := range tests {
		t.Run(name, func(t *testing.T) {
			if _, valid := signer.Verify(value); valid {
				t.Fatalf("Verify(%q) = true, want false", value)
			}
		})
	}
}
