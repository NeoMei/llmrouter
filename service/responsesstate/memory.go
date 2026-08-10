package responsesstate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

// MemoryStore is a single-process Responses state store with a per-record TTL
// and a total in-memory byte ceiling. It is suitable for development and
// single-instance deployments.
type MemoryStore struct {
	mu           sync.RWMutex
	records      map[string]Record
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
		records:  make(map[string]Record),
		ttl:      ttl,
		maxBytes: maxBytes,
	}
}

func (s *MemoryStore) Load(ctx context.Context, tenantID, tokenID int, responseID string) (Record, error) {
	if s == nil {
		return Record{}, ErrNotFound
	}
	key := Key(tenantID, tokenID, responseID)
	s.mu.RLock()
	record, ok := s.records[key]
	s.mu.RUnlock()
	if !ok {
		return Record{}, ErrNotFound
	}
	if time.Now().After(record.ExpiresAt) {
		return Record{}, ErrNotFound
	}
	_ = ctx
	return record, nil
}

func (s *MemoryStore) Save(ctx context.Context, record Record) error {
	if s == nil {
		return errors.New("memory store is nil")
	}
	if err := ValidateRecord(record); err != nil {
		return err
	}
	if time.Now().After(record.ExpiresAt) {
		return errors.New("record is already expired")
	}
	serialized, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("serialize record: %w", err)
	}
	if len(serialized) > MaxRecordBytes {
		return errors.New("responses state record exceeds maximum size")
	}
	key := Key(record.TenantID, record.TokenID, record.ResponseID)

	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.records[key]; ok {
		existingBytes, _ := json.Marshal(existing)
		s.currentBytes -= len(existingBytes)
	}
	if s.currentBytes+len(serialized) > s.maxBytes {
		return errors.New("memory store byte ceiling exceeded")
	}
	s.records[key] = record
	s.currentBytes += len(serialized)
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
	now := time.Now()
	removed := 0
	for key, record := range s.records {
		if now.After(record.ExpiresAt) {
			serialized, _ := json.Marshal(record)
			s.currentBytes -= len(serialized)
			delete(s.records, key)
			removed++
		}
	}
	return removed
}
