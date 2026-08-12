package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
)

type AESGCM struct {
	gcm cipher.AEAD
}

// ParseAESKey accepts a GATEWAY_AES_KEY value in any of the supported
// encodings -- raw bytes, lowercase/uppercase hex, or base64 -- and returns
// the 16/24/32-byte key material. It rejects strings that do not decode to a
// valid AES key size so callers can fail loudly instead of disabling a
// security subsystem.
func ParseAESKey(s string) ([]byte, error) {
	if s == "" {
		return nil, errors.New("empty AES key")
	}
	// Try hex first (the historical key-exchange format).
	if decoded, err := hex.DecodeString(s); err == nil {
		if validAESKeySize(decoded) {
			return decoded, nil
		}
	}
	// Try base64 (common for 32-byte keys stored in env files).
	if decoded, err := base64.StdEncoding.DecodeString(s); err == nil {
		if validAESKeySize(decoded) {
			return decoded, nil
		}
	}
	// Fall back to raw bytes.
	if validAESKeySize([]byte(s)) {
		return []byte(s), nil
	}
	return nil, errors.New("GATEWAY_AES_KEY must decode to a 16, 24, or 32 byte AES key")
}

func validAESKeySize(key []byte) bool {
	return len(key) == 16 || len(key) == 24 || len(key) == 32
}

func NewAESGCM(key []byte) (*AESGCM, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &AESGCM{gcm: gcm}, nil
}

func (a *AESGCM) Encrypt(plaintext []byte) ([]byte, error) {
	nonce := make([]byte, a.gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return a.gcm.Seal(nonce, nonce, plaintext, nil), nil
}

func (a *AESGCM) Decrypt(ciphertext []byte) ([]byte, error) {
	ns := a.gcm.NonceSize()
	if len(ciphertext) < ns {
		return nil, errors.New("ciphertext too short")
	}
	nonce, ct := ciphertext[:ns], ciphertext[ns:]
	return a.gcm.Open(nil, nonce, ct, nil)
}
