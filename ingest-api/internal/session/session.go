package session

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strconv"
	"strings"
	"time"
)

const CookieName = "scrapelog_session"
const Lifetime = 7 * 24 * time.Hour

const keyDerivationMessage = "scrapelog-session"

type Signer struct {
	key []byte
	now func() time.Time
}

func New(adminToken string, now func() time.Time) *Signer {
	keyDeriver := hmac.New(sha256.New, []byte(adminToken))
	keyDeriver.Write([]byte(keyDerivationMessage))
	return &Signer{key: keyDeriver.Sum(nil), now: now}
}

type Claims struct {
	Subject        string
	SessionVersion uint32
}

func (signer *Signer) Issue(subject string, sessionVersion uint32) string {
	expiry := strconv.FormatInt(signer.now().Add(Lifetime).Unix(), 10)
	signedPart := subject + "." + strconv.FormatUint(uint64(sessionVersion), 10) + "." + expiry
	return signedPart + "." + base64.RawURLEncoding.EncodeToString(signer.mac(signedPart))
}

func (signer *Signer) Verify(value string) (Claims, bool) {
	parts := strings.Split(value, ".")
	if len(parts) != 4 || parts[0] == "" {
		return Claims{}, false
	}
	presentedMAC, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil {
		return Claims{}, false
	}
	if !hmac.Equal(presentedMAC, signer.mac(value[:strings.LastIndex(value, ".")])) {
		return Claims{}, false
	}
	sessionVersion, err := strconv.ParseUint(parts[1], 10, 32)
	if err != nil {
		return Claims{}, false
	}
	expiry, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return Claims{}, false
	}
	if signer.now().Unix() >= expiry {
		return Claims{}, false
	}
	return Claims{Subject: parts[0], SessionVersion: uint32(sessionVersion)}, true
}

func (signer *Signer) mac(message string) []byte {
	messageAuthenticator := hmac.New(sha256.New, signer.key)
	messageAuthenticator.Write([]byte(message))
	return messageAuthenticator.Sum(nil)
}
