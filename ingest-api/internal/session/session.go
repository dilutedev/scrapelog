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

func (signer *Signer) Issue() string {
	expiry := strconv.FormatInt(signer.now().Add(Lifetime).Unix(), 10)
	return expiry + "." + base64.RawURLEncoding.EncodeToString(signer.mac(expiry))
}

func (signer *Signer) Valid(value string) bool {
	separatorIndex := strings.LastIndex(value, ".")
	if separatorIndex <= 0 || separatorIndex == len(value)-1 {
		return false
	}
	expiryText := value[:separatorIndex]
	expiry, err := strconv.ParseInt(expiryText, 10, 64)
	if err != nil {
		return false
	}
	presentedMAC, err := base64.RawURLEncoding.DecodeString(value[separatorIndex+1:])
	if err != nil {
		return false
	}
	if !hmac.Equal(presentedMAC, signer.mac(expiryText)) {
		return false
	}
	return signer.now().Unix() < expiry
}

func (signer *Signer) mac(message string) []byte {
	messageAuthenticator := hmac.New(sha256.New, signer.key)
	messageAuthenticator.Write([]byte(message))
	return messageAuthenticator.Sum(nil)
}
