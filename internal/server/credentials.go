package server

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"omenswap.com/swap-aggregator/internal/provider"
)

const (
	credentialCookie = "swap_provider_keys"
	credentialTTL    = 8 * time.Hour
	maxAPIKeyBytes   = 2048
)

type credentialEnvelope struct {
	Keys      map[string]string `json:"keys"`
	ExpiresAt int64             `json:"expires_at"`
}

func newCredentialKey() []byte {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic("generate provider credential encryption key: " + err.Error())
	}
	return key
}

func (s *Server) credentialAEAD() cipher.AEAD {
	block, err := aes.NewCipher(s.credentialKey)
	if err != nil {
		panic("create provider credential cipher: " + err.Error())
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		panic("create provider credential AEAD: " + err.Error())
	}
	return aead
}

func (s *Server) readCredentials(r *http.Request) map[string]string {
	cookie, err := r.Cookie(credentialCookie)
	if err != nil || cookie.Value == "" {
		return map[string]string{}
	}
	raw, err := base64.RawURLEncoding.DecodeString(cookie.Value)
	if err != nil {
		return map[string]string{}
	}
	aead := s.credentialAEAD()
	if len(raw) < aead.NonceSize() {
		return map[string]string{}
	}
	plain, err := aead.Open(nil, raw[:aead.NonceSize()], raw[aead.NonceSize():], nil)
	if err != nil {
		return map[string]string{}
	}
	var envelope credentialEnvelope
	if json.Unmarshal(plain, &envelope) != nil || envelope.ExpiresAt < time.Now().Unix() || envelope.Keys == nil {
		return map[string]string{}
	}
	return envelope.Keys
}

func (s *Server) writeCredentials(w http.ResponseWriter, r *http.Request, keys map[string]string) error {
	envelope := credentialEnvelope{Keys: keys, ExpiresAt: time.Now().Add(credentialTTL).Unix()}
	plain, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	aead := s.credentialAEAD()
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	sealed := aead.Seal(nonce, nonce, plain, nil)
	encoded := base64.RawURLEncoding.EncodeToString(sealed)
	// Stay below common 4 KiB per-cookie limits, including cookie attributes.
	if len(encoded) > 3800 {
		return errors.New("provider credentials exceed cookie capacity")
	}
	http.SetCookie(w, &http.Cookie{
		Name:     credentialCookie,
		Value:    encoded,
		Path:     "/",
		HttpOnly: true,
		Secure:   r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https"),
		SameSite: http.SameSiteStrictMode,
	})
	return nil
}

// providerForRequest returns a short-lived clone carrying the visitor's key.
// The configured provider is never mutated, which keeps credentials isolated
// between concurrent requests.
func (s *Server) providerForRequest(r *http.Request, base provider.Provider) (provider.Provider, bool) {
	gated, gatedOK := base.(provider.Gated)
	credentialed, credentialedOK := base.(provider.Credentialed)
	if !gatedOK || gated.Brokered() || !credentialedOK {
		return base, false
	}
	key := s.readCredentials(r)[base.Name()]
	if key == "" {
		return base, false
	}
	p, err := credentialed.WithAPIKey(key)
	if err != nil {
		return base, false
	}
	return p, true
}

func apiKeyRequirements(p provider.Provider) (quote, swap bool) {
	gated, gatedOK := p.(provider.Gated)
	if !gatedOK || gated.Brokered() {
		return false, false
	}
	if policy, ok := p.(provider.APIKeyPolicy); ok {
		return policy.APIKeyRequiredForQuote(), policy.APIKeyRequiredForSwap()
	}
	// Backward-compatible default for older gated providers: quotes are public,
	// but the aggregator cannot create a swap without a key.
	return false, true
}

func needsUserAPIKey(p provider.Provider) bool {
	quote, swap := apiKeyRequirements(p)
	_, credentialedOK := p.(provider.Credentialed)
	return (quote || swap) && credentialedOK
}

func (s *Server) handleProviderKey(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxAPIKeyBytes+1024)
	var input struct {
		Provider string `json:"provider"`
		APIKey   string `json:"api_key"`
	}
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&input); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "api key is too large"})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	input.Provider = strings.TrimSpace(input.Provider)
	input.APIKey = strings.TrimSpace(input.APIKey)
	if input.Provider == "" || input.APIKey == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "provider and api_key are required"})
		return
	}
	if len(input.APIKey) > maxAPIKeyBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "api key is too large"})
		return
	}
	base, ok := s.byName[input.Provider]
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown provider"})
		return
	}
	if !needsUserAPIKey(base) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "provider does not accept a user API key"})
		return
	}
	credentialed := base.(provider.Credentialed)
	if _, err := credentialed.WithAPIKey(input.APIKey); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	keys := s.readCredentials(r)
	keys[input.Provider] = input.APIKey
	if err := s.writeCredentials(w, r, keys); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not protect api key"})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
}
