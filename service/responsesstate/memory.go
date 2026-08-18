package responsesstate

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
)

// MemoryStore is a single-process Responses state store with a per-record TTL
// and a total in-memory byte ceiling. It is suitable for development and
// single-instance deployments.
type MemoryStore struct {
	mu           sync.RWMutex
	records      map[string]Record
	responseKeys map[string]map[string]struct{}
	ttl          time.Duration
	maxBytes     int
	currentBytes int
}

// NewMemory creates an in-memory store. ttl controls per-record expiry; maxBytes
// limits the total serialized size of all stored records.
func NewMemory(ttl time.Duration, maxBytes int) *MemoryStore {
	if ttl <= 0 {
		ttl = DefaultRecordTTL
	}
	if maxBytes <= 0 {
		maxBytes = 32 * 1024 * 1024
	}
	return &MemoryStore{
		records:      make(map[string]Record),
		responseKeys: make(map[string]map[string]struct{}),
		ttl:          ttl,
		maxBytes:     maxBytes,
	}
}

func (s *MemoryStore) Load(ctx context.Context, expected Scope, responseID string) (Record, error) {
	if s == nil {
		return Record{}, ErrNotFound
	}
	bucketKey := Key(responseID)
	s.mu.Lock()
	s.pruneExpiredLocked(time.Now())
	keys := s.responseKeys[bucketKey]
	candidates := make([]Record, 0, len(keys))
	if expected.ClientTaskID != "" {
		if record, ok := s.records[scopedKey(expected, responseID)]; ok {
			candidates = append(candidates, record)
		}
	} else {
		for key := range keys {
			if record, ok := s.records[key]; ok {
				candidates = append(candidates, record)
			}
		}
	}
	_, legacy := s.records[legacyKey(expected.TenantID, expected.TokenID, responseID)]
	bucketHasRecords := len(keys) != 0
	s.mu.Unlock()
	if len(candidates) == 0 {
		if bucketHasRecords {
			return Record{}, ErrScopeMismatch
		}
		if legacy {
			return Record{}, ErrLegacyScope
		}
		return Record{}, ErrNotFound
	}

	now := time.Now()
	matches := make([]Record, 0, 1)
	hadScopeMatch := false
	for _, record := range candidates {
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
	cloned, err := cloneRecord(matches[0])
	if err != nil {
		return Record{}, fmt.Errorf("copy record: %w", err)
	}
	_ = ctx
	return cloned, nil
}

func (s *MemoryStore) Save(ctx context.Context, record Record) error {
	if s == nil {
		return errors.New("memory store is nil")
	}
	if record.ExpiresAt.IsZero() {
		record.ExpiresAt = time.Now().Add(s.ttl)
	}
	if err := ValidateRecord(record); err != nil {
		return err
	}
	if time.Now().After(record.ExpiresAt) {
		return errors.New("record is already expired")
	}
	serialized, err := common.Marshal(record)
	if err != nil {
		return fmt.Errorf("serialize record: %w", err)
	}
	if len(serialized) > MaxRecordBytes {
		return errors.New("responses state record exceeds maximum size")
	}
	stored, err := cloneRecord(record)
	if err != nil {
		return fmt.Errorf("copy record: %w", err)
	}
	bucketKey := Key(record.ResponseID)
	key := scopedKey(record.Scope, record.ResponseID)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneExpiredLocked(time.Now())
	existingSize := 0
	if existing, ok := s.records[key]; ok {
		existingBytes, _ := common.Marshal(existing)
		existingSize = len(existingBytes)
	}
	nextBytes := s.currentBytes - existingSize + len(serialized)
	if nextBytes > s.maxBytes {
		return errors.New("memory store byte ceiling exceeded")
	}
	s.records[key] = stored
	if s.responseKeys[bucketKey] == nil {
		s.responseKeys[bucketKey] = make(map[string]struct{})
	}
	s.responseKeys[bucketKey][key] = struct{}{}
	s.currentBytes = nextBytes
	_ = ctx
	return nil
}

// Prune removes expired records and returns the count.
func (s *MemoryStore) Prune() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pruneExpiredLocked(time.Now())
}

func (s *MemoryStore) pruneExpiredLocked(now time.Time) int {
	removed := 0
	for bucketKey, keys := range s.responseKeys {
		for key := range keys {
			record, ok := s.records[key]
			if ok && now.After(record.ExpiresAt) {
				serialized, _ := common.Marshal(record)
				s.currentBytes -= len(serialized)
				delete(s.records, key)
				delete(keys, key)
				removed++
			}
		}
		if len(keys) == 0 {
			delete(s.responseKeys, bucketKey)
		}
	}
	return removed
}

func cloneRecord(record Record) (Record, error) {
	data, err := common.Marshal(record)
	if err != nil {
		return Record{}, err
	}
	var cloned Record
	if err := common.Unmarshal(data, &cloned); err != nil {
		return Record{}, err
	}
	return cloned, nil
}
