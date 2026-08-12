package responsesstate

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/go-redis/redis/v8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/crypto"
)

// RedisStore is a multi-instance Responses state store backed by Redis. Records
// are encrypted at rest with AES-GCM using GATEWAY_AES_KEY when set.
type RedisStore struct {
	key []byte
}

// NewRedis creates a Redis-backed store. It returns an error if encryption is
// requested but no key is configured.
func NewRedis() (*RedisStore, error) {
	k := os.Getenv("GATEWAY_AES_KEY")
	if k == "" {
		return nil, errors.New("GATEWAY_AES_KEY is required for Redis responses state store")
	}
	key, err := crypto.ParseAESKey(k)
	if err != nil {
		return nil, err
	}
	return &RedisStore{key: key}, nil
}

func (s *RedisStore) Load(ctx context.Context, tenantID, tokenID int, responseID string) (Record, error) {
	if s == nil || common.RDB == nil {
		return Record{}, ErrNotFound
	}
	key := Key(tenantID, tokenID, responseID)
	val, err := common.RedisGet(key)
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return Record{}, ErrNotFound
		}
		return Record{}, err
	}
	data, err := s.decrypt([]byte(val))
	if err != nil {
		return Record{}, err
	}
	var record Record
	if err := json.Unmarshal(data, &record); err != nil {
		return Record{}, fmt.Errorf("decode record: %w", err)
	}
	if time.Now().After(record.ExpiresAt) {
		return Record{}, ErrNotFound
	}
	return record, nil
}

func (s *RedisStore) Save(ctx context.Context, record Record) error {
	if s == nil || common.RDB == nil {
		return errors.New("Redis is not available")
	}
	if err := ValidateRecord(record); err != nil {
		return err
	}
	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("serialize record: %w", err)
	}
	if len(data) > MaxRecordBytes {
		return errors.New("responses state record exceeds maximum size")
	}
	encrypted, err := s.encrypt(data)
	if err != nil {
		return err
	}
	ttl := time.Until(record.ExpiresAt)
	if ttl <= 0 {
		ttl = DefaultRecordTTL
	}
	key := Key(record.TenantID, record.TokenID, record.ResponseID)
	return common.RedisSet(key, string(encrypted), ttl)
}

func (s *RedisStore) encrypt(plaintext []byte) ([]byte, error) {
	if len(s.key) == 0 {
		return plaintext, nil
	}
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

func (s *RedisStore) decrypt(ciphertext []byte) ([]byte, error) {
	if len(s.key) == 0 {
		return ciphertext, nil
	}
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(ciphertext) < gcm.NonceSize() {
		return nil, errors.New("ciphertext too short")
	}
	nonce, text := ciphertext[:gcm.NonceSize()], ciphertext[gcm.NonceSize():]
	return gcm.Open(nil, nonce, text, nil)
}
