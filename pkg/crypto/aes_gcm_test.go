package crypto

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseAESKeyRawBytes(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	got, err := ParseAESKey(string(key))
	require.NoError(t, err)
	assert.True(t, bytes.Equal(key, got))
}

func TestParseAESKeyHex(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	got, err := ParseAESKey(hex.EncodeToString(key))
	require.NoError(t, err)
	assert.True(t, bytes.Equal(key, got))
}

func TestParseAESKeyBase64(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	got, err := ParseAESKey(base64.StdEncoding.EncodeToString(key))
	require.NoError(t, err)
	assert.True(t, bytes.Equal(key, got))
}

func TestParseAESKeyRejectsInvalidLength(t *testing.T) {
	_, err := ParseAESKey("too-short")
	require.Error(t, err)
	_, err = ParseAESKey(base64.StdEncoding.EncodeToString([]byte("short")))
	require.Error(t, err)
}

func TestEncryptDecryptRoundtrip(t *testing.T) {
	key := make([]byte, 32) // AES-256
	c, err := NewAESGCM(key)
	require.NoError(t, err)

	plaintext := []byte("sk-test-api-key-12345")
	encrypted, err := c.Encrypt(plaintext)
	require.NoError(t, err)

	decrypted, err := c.Decrypt(encrypted)
	require.NoError(t, err)
	assert.True(t, bytes.Equal(plaintext, decrypted), "roundtrip mismatch")
}

func TestEncryptProducesDifferentOutput(t *testing.T) {
	key := make([]byte, 32)
	c, err := NewAESGCM(key)
	require.NoError(t, err)

	pt := []byte("same-input")
	e1, err := c.Encrypt(pt)
	require.NoError(t, err)
	e2, err := c.Encrypt(pt)
	require.NoError(t, err)

	assert.False(t, bytes.Equal(e1, e2), "expected different ciphertext due to random nonce")
}

func TestDecryptRejectsShortCiphertext(t *testing.T) {
	key := make([]byte, 32)
	c, err := NewAESGCM(key)
	require.NoError(t, err)

	_, err = c.Decrypt([]byte("short"))
	require.Error(t, err)
}

func TestDecryptDetectsTamper(t *testing.T) {
	key := make([]byte, 32)
	c, err := NewAESGCM(key)
	require.NoError(t, err)

	enc, err := c.Encrypt([]byte("secret"))
	require.NoError(t, err)

	// ponytail: flip a byte inside the ciphertext body to trigger GCM auth failure
	tampered := append([]byte{}, enc...)
	tampered[len(tampered)-1] ^= 0x01
	_, err = c.Decrypt(tampered)
	require.Error(t, err)
}

func TestNewAESGCMRejectsInvalidKeyLength(t *testing.T) {
	_, err := NewAESGCM([]byte("too-short"))
	require.Error(t, err)
}
