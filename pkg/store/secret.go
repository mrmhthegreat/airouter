package store

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/mrmhthegreat/airouter/pkg/types"
)

// How secrets are kept
//
//   - Tenant API keys (air_...) are random 192-bit tokens. Only their SHA-256
//     hash is stored (see HashKey); the key itself is shown once and cannot be
//     recovered, even by you.
//   - Provider API keys (Groq, Gemini, ...) must be sent upstream, so they
//     cannot be hashed. They are encrypted with AES-256-GCM before they reach
//     the database and decrypted in memory. A stolen airouter.db alone does
//     not reveal them; the attacker also needs the master secret.
//
// The master secret comes from $AIROUTER_SECRET, or (if unset) from a
// 0600-permission key file created next to the database. For servers, set
// $AIROUTER_SECRET and keep it out of the same backup as the DB.

const encPrefix = "enc:v1:"

// EncryptedStore wraps any Store and transparently encrypts
// ProviderConfig.APIKey at rest. Everything else passes straight through.
type EncryptedStore struct {
	Store
	aead cipher.AEAD
}

// WithEncryption wraps inner so provider keys are encrypted at rest. secret is
// any non-empty byte string (a 32-byte random value is best); it is stretched
// to an AES-256 key with SHA-256.
func WithEncryption(inner Store, secret []byte) (*EncryptedStore, error) {
	if len(secret) == 0 {
		return nil, errors.New("store: empty encryption secret")
	}
	sum := sha256.Sum256(append([]byte("airouter/provider-key/v1:"), secret...))
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &EncryptedStore{Store: inner, aead: aead}, nil
}

func (e *EncryptedStore) seal(plain string) (string, error) {
	if plain == "" || strings.HasPrefix(plain, encPrefix) {
		return plain, nil
	}
	nonce := make([]byte, e.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ct := e.aead.Seal(nonce, nonce, []byte(plain), nil)
	return encPrefix + base64.RawStdEncoding.EncodeToString(ct), nil
}

func (e *EncryptedStore) open(stored string) (string, error) {
	if !strings.HasPrefix(stored, encPrefix) {
		return stored, nil // legacy plaintext; re-encrypted by EncryptExisting
	}
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(stored, encPrefix))
	if err != nil || len(raw) < e.aead.NonceSize() {
		return "", errors.New("store: corrupt encrypted provider key")
	}
	n := e.aead.NonceSize()
	pt, err := e.aead.Open(nil, raw[:n], raw[n:], nil)
	if err != nil {
		return "", errors.New("store: cannot decrypt provider key — wrong AIROUTER_SECRET / key file for this database?")
	}
	return string(pt), nil
}

func (e *EncryptedStore) SaveProvider(ctx context.Context, p *types.ProviderConfig) error {
	cp := *p
	enc, err := e.seal(cp.APIKey)
	if err != nil {
		return err
	}
	cp.APIKey = enc
	if err := e.Store.SaveProvider(ctx, &cp); err != nil {
		return err
	}
	p.ID = cp.ID
	return nil
}

func (e *EncryptedStore) ListProviders(ctx context.Context) ([]types.ProviderConfig, error) {
	ps, err := e.Store.ListProviders(ctx)
	if err != nil {
		return nil, err
	}
	for i := range ps {
		k, err := e.open(ps[i].APIKey)
		if err != nil {
			return nil, fmt.Errorf("provider %q: %w", ps[i].Name, err)
		}
		ps[i].APIKey = k
	}
	return ps, nil
}

// EncryptExisting re-saves any provider whose key is still stored as
// plaintext (e.g. a database created by an older version) so it becomes
// encrypted. Safe to call on every start. Returns how many rows it upgraded.
func (e *EncryptedStore) EncryptExisting(ctx context.Context) (int, error) {
	ps, err := e.Store.ListProviders(ctx) // raw: no decryption
	if err != nil {
		return 0, err
	}
	n := 0
	for i := range ps {
		if ps[i].APIKey == "" || strings.HasPrefix(ps[i].APIKey, encPrefix) {
			continue
		}
		enc, err := e.seal(ps[i].APIKey)
		if err != nil {
			return n, err
		}
		ps[i].APIKey = enc
		if err := e.Store.SaveProvider(ctx, &ps[i]); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// MasterSecret finds the encryption secret: $AIROUTER_SECRET if set, otherwise
// a random key file at keyFile (created with mode 0600 on first use). source
// describes where it came from, for logging. Never log the secret itself.
func MasterSecret(keyFile string) (secret []byte, source string, err error) {
	if v := strings.TrimSpace(os.Getenv("AIROUTER_SECRET")); v != "" {
		return []byte(v), "$AIROUTER_SECRET", nil
	}
	if b, err := os.ReadFile(keyFile); err == nil {
		if s := strings.TrimSpace(string(b)); s != "" {
			return []byte(s), keyFile, nil
		}
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, "", err
	}
	s := hex.EncodeToString(raw)
	if err := os.WriteFile(keyFile, []byte(s+"\n"), 0o600); err != nil {
		return nil, "", fmt.Errorf("creating key file %s: %w", keyFile, err)
	}
	return []byte(s), keyFile + " (new)", nil
}
