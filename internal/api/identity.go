package api

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"net/http"
	"time"
)

const cookieName = "efir_voter"
const identityLifetime = 365 * 24 * time.Hour

type identity struct {
	signing, dedup []byte
	secure         bool
}

func (i identity) issue(w http.ResponseWriter, now time.Time) error {
	// version | expiry seconds | 128 random bits | 256-bit signature
	payload := make([]byte, 25)
	payload[0] = 1
	expires := now.Add(identityLifetime)
	binary.BigEndian.PutUint64(payload[1:9], uint64(expires.Unix()))
	if _, err := rand.Read(payload[9:]); err != nil {
		return err
	}
	mac := hmac.New(sha256.New, i.signing)
	mac.Write(payload)
	token := base64.RawURLEncoding.EncodeToString(append(payload, mac.Sum(nil)...))
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: token, Path: "/", HttpOnly: true, Secure: i.secure, SameSite: http.SameSiteLaxMode, MaxAge: int(identityLifetime.Seconds()), Expires: expires})
	return nil
}

func (i identity) read(r *http.Request, now time.Time) ([]byte, error) {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return nil, err
	}
	if len(c.Value) != 76 {
		return nil, errors.New("invalid identity")
	}
	b, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil || len(b) != 57 || b[0] != 1 {
		return nil, errors.New("invalid identity")
	}
	mac := hmac.New(sha256.New, i.signing)
	mac.Write(b[:25])
	if !hmac.Equal(mac.Sum(nil), b[25:]) {
		return nil, errors.New("invalid signature")
	}
	expiry := binary.BigEndian.Uint64(b[1:9])
	if expiry <= uint64(now.Unix()) || expiry > uint64(now.Add(identityLifetime+time.Minute).Unix()) {
		return nil, errors.New("expired identity")
	}
	return b[9:25], nil
}

func (i identity) digest(pollID string, voter []byte) []byte {
	mac := hmac.New(sha256.New, i.dedup)
	mac.Write([]byte("efir-ballot-v1\x00"))
	mac.Write([]byte(pollID))
	mac.Write([]byte{0})
	mac.Write(voter)
	return mac.Sum(nil)
}
