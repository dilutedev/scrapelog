package session

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

var baseTime = time.Unix(1_700_000_000, 0)

func clockAt(currentTime *time.Time) func() time.Time {
	return func() time.Time { return *currentTime }
}

func TestValidAcceptsIssuedValueUntilExpiry(t *testing.T) {
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
			value := signer.Issue()
			currentTime = currentTime.Add(test.advance)
			if got := signer.Valid(value); got != test.valid {
				t.Fatalf("Valid = %v, want %v", got, test.valid)
			}
		})
	}
}

func TestValidRejectsTamperedAndForeignValues(t *testing.T) {
	currentTime := baseTime
	signer := New("admin-token", clockAt(&currentTime))
	issued := signer.Issue()
	separatorIndex := strings.LastIndex(issued, ".")
	expiryText, macText := issued[:separatorIndex], issued[separatorIndex+1:]
	extendedExpiry := strconv.FormatInt(baseTime.Add(2*Lifetime).Unix(), 10)
	flippedMAC := "A" + macText[1:]
	if flippedMAC == macText {
		flippedMAC = "B" + macText[1:]
	}
	otherSigner := New("other-token", clockAt(&currentTime))

	tests := map[string]string{
		"tampered mac":       expiryText + "." + flippedMAC,
		"tampered expiry":    extendedExpiry + "." + macText,
		"other token":        otherSigner.Issue(),
		"empty":              "",
		"no dot":             "nodothere",
		"leading dot":        "." + macText,
		"trailing dot":       expiryText + ".",
		"non numeric expiry": "abc." + macText,
		"bad base64":         expiryText + ".!!!not-base64!!!",
		"extra dots":         expiryText + "." + expiryText + "." + macText,
	}
	for name, value := range tests {
		t.Run(name, func(t *testing.T) {
			if signer.Valid(value) {
				t.Fatalf("Valid(%q) = true, want false", value)
			}
		})
	}
}
