package platformbilling

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"gorm.io/gorm"
)

// RejectAliMissingChatModel accepts only an error-only response from Ali's
// authenticated compatible Chat endpoint. Unknown transport/provider outcomes
// stay reserved. The proof and release intent are persisted together so a
// crash after observing rejection cannot leave a permanent customer hold.
// Response bytes remain available to the normal error handler.
func (s *Session) RejectAliMissingChatModel(ctx context.Context, channelType, channelID int, response *http.Response, insecureTLS bool) (bool, error) {
	if !s.NativePricing() || channelType != constant.ChannelTypeAli || insecureTLS || response == nil || response.StatusCode != http.StatusNotFound || response.Request == nil || response.Request.URL == nil || response.Body == nil {
		return false, nil
	}
	req := response.Request
	if req.Method != http.MethodPost || req.URL.String() != "https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions" || (req.Host != "" && req.Host != "dashscope.aliyuncs.com") {
		return false, nil
	}
	contentType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		return false, nil
	}
	quote, err := s.NativeQuote()
	if err != nil || quote.WireOperation != "chat.completions.create" || quote.ChannelID != strconv.Itoa(channelID) || s.attempt.ID == 0 || s.attempt.ChannelID != channelID || s.call.Status != "UNKNOWN" || s.err != nil || s.confirmed != nil {
		return false, nil
	}
	original := response.Body
	raw, readErr := io.ReadAll(io.LimitReader(original, (16<<10)+1))
	response.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(raw), original), original}
	if readErr != nil || len(raw) > 16<<10 {
		return false, nil
	}
	var fields map[string]json.RawMessage
	if common.UnmarshalUnique(raw, &fields) != nil || len(fields) == 0 {
		return false, nil
	}
	for field := range fields {
		if field != "error" && field != "request_id" {
			return false, nil
		}
	}
	var providerError map[string]json.RawMessage
	if common.UnmarshalUnique(fields["error"], &providerError) != nil || len(providerError) == 0 {
		return false, nil
	}
	for field := range providerError {
		if field != "code" && field != "message" && field != "type" && field != "param" {
			return false, nil
		}
	}
	var code, message, kind string
	if common.Unmarshal(providerError["code"], &code) != nil || code != "model_not_found" || common.Unmarshal(providerError["message"], &message) != nil || strings.TrimSpace(message) == "" {
		return false, nil
	}
	if value, exists := providerError["type"]; exists && (common.Unmarshal(value, &kind) != nil || kind != "invalid_request_error") {
		return false, nil
	}
	if value, exists := providerError["param"]; exists && string(value) != "null" && string(value) != `"model"` {
		return false, nil
	}
	var requestID string
	if value, exists := fields["request_id"]; exists && common.Unmarshal(value, &requestID) != nil {
		return false, nil
	}
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	proof, err := common.Marshal(map[string]any{"kind": "supplier_rejection", "provider": "aliyun", "operation": "chat.completions.create", "channel_id": channelID, "endpoint": req.URL.String(), "status": 404, "code": code, "body_sha256": digest, "request_id": requestID})
	if err != nil {
		return false, err
	}
	intent, err := common.Marshal(map[string]string{"reason": "ali_model_not_found:" + digest})
	if err != nil {
		return false, err
	}
	err = s.adapter.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var attempt Attempt
		if e := platformBillingLockedRead(tx).First(&attempt, s.attempt.ID).Error; e != nil {
			return e
		}
		var row Call
		if e := platformBillingLockedRead(tx).First(&row, "call_id = ?", s.call.CallID).Error; e != nil {
			return e
		}
		// Timestamps are not billing identity: PostgreSQL rounds CreatedAt to
		// microseconds, and reserve/dispatch advance only the persisted UpdatedAt.
		// Every business, pricing and evidence field must still match exactly.
		expected := s.call
		expected.CreatedAt, expected.UpdatedAt = row.CreatedAt, row.UpdatedAt
		if attempt.CallID != row.CallID || attempt.ChannelID != channelID || attempt.Status != "DISPATCHED" || !waiverCallEqual(row, expected) || row.Status != "UNKNOWN" || row.Evidence != "" || row.Intent != "" || row.EvidenceConflict || row.NativeObservation != "" || row.ImageObservation != "" || row.OCRObservation != "" || row.AudioObservation != "" || (row.Charged != "" && row.Charged != "0") {
			return ErrConflict
		}
		if e := tx.Model(&attempt).Update("status", "REJECTED").Error; e != nil {
			return e
		}
		return tx.Model(&row).Updates(map[string]any{"status": "RELEASE_PENDING", "evidence": string(proof), "intent": string(intent)}).Error
	})
	if err != nil {
		s.err = err
		return false, err
	}
	s.call.Status, s.call.Evidence, s.call.Intent = "RELEASE_PENDING", string(proof), string(intent)
	s.attempt.Status = "REJECTED"
	s.safeFailure, s.uncertain = true, false
	return true, nil
}
