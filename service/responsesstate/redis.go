package responsesstate

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
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

func (s *RedisStore) Load(ctx context.Context, expected Scope, responseID string) (Record, error) {
	if s == nil || common.RDB == nil {
		return Record{}, ErrNotFound
	}
	bucketKey := Key(responseID)
	values := make([]string, 0, 1)
	bucketHasRecords := false
	if expected.ClientTaskID != "" {
		value, err := common.RDB.HGet(ctx, bucketKey, scopeDigest(expected)).Result()
		if err == nil {
			values = append(values, value)
			bucketHasRecords = true
		} else if errors.Is(err, redis.Nil) {
			count, countErr := common.RDB.HLen(ctx, bucketKey).Result()
			if countErr != nil {
				return Record{}, countErr
			}
			bucketHasRecords = count > 0
		} else {
			return Record{}, err
		}
	} else {
		entries, err := common.RDB.HGetAll(ctx, bucketKey).Result()
		if err != nil {
			return Record{}, err
		}
		bucketHasRecords = len(entries) > 0
		for _, value := range entries {
			values = append(values, value)
		}
	}
	if len(values) == 0 {
		if bucketHasRecords {
			return Record{}, ErrScopeMismatch
		}
		if _, legacyErr := common.RedisGet(legacyKey(expected.TenantID, expected.TokenID, responseID)); legacyErr == nil {
			return Record{}, ErrLegacyScope
		} else if !errors.Is(legacyErr, redis.Nil) {
			return Record{}, legacyErr
		}
		return Record{}, ErrNotFound
	}

	now := time.Now()
	matches := make([]Record, 0, 1)
	hadScopeMatch := false
	for _, value := range values {
		data, err := s.decrypt([]byte(value))
		if err != nil {
			return Record{}, err
		}
		var record Record
		if err := common.Unmarshal(data, &record); err != nil {
			return Record{}, fmt.Errorf("decode record: %w", err)
		}
		if err := ValidateRecord(record); err != nil {
			return Record{}, err
		}
		if record.ResponseID != responseID || !scopeMatches(expected, record.Scope) {
			continue
		}
		hadScopeMatch = true
		if now.After(record.ExpiresAt) {
			continue
		}
		matches = append(matches, record)
	}
	if len(matches) == 0 {
		if hadScopeMatch {
			return Record{}, ErrNotFound
		}
		return Record{}, ErrScopeMismatch
	}
	if len(matches) != 1 {
		return Record{}, ErrScopeMismatch
	}
	return matches[0], nil
}

func (s *RedisStore) Save(ctx context.Context, record Record) error {
	if s == nil || common.RDB == nil {
		return errors.New("Redis is not available")
	}
	if record.ExpiresAt.IsZero() {
		record.ExpiresAt = time.Now().Add(DefaultRecordTTL)
	}
	if !record.ExpiresAt.After(time.Now()) {
		return errors.New("record is already expired")
	}
	if err := ValidateRecord(record); err != nil {
		return err
	}
	data, err := common.Marshal(record)
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
		return errors.New("record is already expired")
	}
	ttlMillis := ttl.Milliseconds()
	if ttlMillis < 1 {
		ttlMillis = 1
	}
	const saveRecordScript = `
local current_ttl = redis.call('PTTL', KEYS[1])
redis.call('HSET', KEYS[1], ARGV[1], ARGV[2])
if current_ttl < 0 or current_ttl < tonumber(ARGV[3]) then
  redis.call('PEXPIRE', KEYS[1], ARGV[3])
end
return 1`
	return common.RDB.Eval(ctx, saveRecordScript, []string{Key(record.ResponseID)}, scopeDigest(record.Scope), string(encrypted), ttlMillis).Err()
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
