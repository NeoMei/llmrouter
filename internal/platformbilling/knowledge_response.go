package platformbilling

// This validator is intentionally not connected to a relay or result vault.
// A future integration must recheck its binding during atomic persistence and
// preserve real usage even if response validation or delivery fails.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"math"
	"math/big"
	"net/http"
	"strconv"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/pkg/metering"
	"gorm.io/gorm"
)

type knowledgeResponse struct {
	rawDigest     [32]byte
	snapshot      Call
	attempt       Attempt
	callID        string
	attemptID     uint
	requestDigest string
	wire          WireOperation
	body          []byte
	usage         metering.Usage
}

// validateKnowledgeResponse consumes a bounded raw provider response for the
// active, already-dispatched session. It performs no network retry, write,
// billing or release, and returns no partial result on malformed responses.
func validateKnowledgeResponse(ctx context.Context, s *Session, channel int, upstream, key string, response *http.Response) (knowledgeResponse, error) {
	if s == nil || s.adapter == nil || s.adapter.db == nil || s.attempt.ID == 0 || response == nil || response.Body == nil {
		return knowledgeResponse{}, ErrInvalid
	}
	defer response.Body.Close()
	// Session is single-owner; use one value snapshot throughout validation.
	snapshot := *s
	s = &snapshot
	if response.StatusCode != http.StatusOK || s.attempt.ChannelID != channel || s.attempt.Status != "DISPATCHED" {
		return knowledgeResponse{}, ErrInvalid
	}
	if _, err := s.KnowledgeBody(channel, upstream, key); err != nil {
		return knowledgeResponse{}, ErrInvalid
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, knowledgeResultMaxBytes+1))
	if err != nil || len(raw) == 0 || len(raw) > knowledgeResultMaxBytes || !utf8.Valid(raw) || ctx.Err() != nil {
		return knowledgeResponse{}, ErrInvalid
	}
	var wire map[string]json.RawMessage
	if common.UnmarshalUnique(raw, &wire) != nil || wire == nil || len(wire["error"]) != 0 {
		return knowledgeResponse{}, ErrInvalid
	}
	var row Call
	var verifiedAttempt Attempt
	err = s.adapter.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&Call{}).Where("call_id = ?", s.call.CallID).UpdateColumn("updated_at", gorm.Expr("updated_at")).Error; err != nil {
			return ErrPending
		}
		if err := tx.First(&row, "call_id = ?", s.call.CallID).Error; err != nil {
			return ErrPending
		}
		if err := tx.First(&verifiedAttempt, "id = ? AND call_id = ?", s.attempt.ID, s.call.CallID).Error; err != nil || verifiedAttempt.Status != "DISPATCHED" || verifiedAttempt.ChannelID != channel || verifiedAttempt.UpstreamIdentity != s.attempt.UpstreamIdentity {
			return ErrPending
		}
		return nil
	})
	if err != nil {
		return knowledgeResponse{}, err
	}
	if row.Status != "UNKNOWN" || row.EvidenceConflict || !sameKnowledgeCallBinding(row, s.call) {
		return knowledgeResponse{}, ErrPending
	}
	var frozen frozenKnowledge
	var rate Rate
	if common.UnmarshalUnique([]byte(row.MediaSnapshot), &frozen) != nil || !frozen.Policy.valid(s.adapter.config, row.Model, row.WireOperation) || common.UnmarshalUnique([]byte(row.RateSnapshot), &rate) != nil || rate.Metered == nil {
		return knowledgeResponse{}, ErrInvalid
	}
	asset, err := normalizeAsset(row.Asset)
	if err != nil || rate.ID != row.RateID || rate.Model != row.Model || rate.Asset != asset || rate.Status != "published" || rate.Semantics != "metered_v2" || frozen.Policy.RateIDs[asset] != rate.ID || rate.Metered.Operation != row.Operation || metering.ValidatePrice(*rate.Metered) != nil {
		return knowledgeResponse{}, ErrInvalid
	}
	_, certified := TokenProducer(row.WireOperation, frozen.Policy.ChannelType)
	if !certified || frozen.Policy.ChannelID != channel || frozen.Policy.UpstreamModel != upstream || row.ReservationQuantity < 1 {
		return knowledgeResponse{}, ErrInvalid
	}
	result := knowledgeResponse{callID: row.CallID, attemptID: s.attempt.ID, requestDigest: row.Digest, wire: s.WireOperation()}
	switch row.WireOperation {
	case "embeddings.create":
		var request map[string]any
		if common.UnmarshalUnique(frozen.Body, &request) != nil {
			return knowledgeResponse{}, ErrInvalid
		}
		count, err := (&dto.EmbeddingRequest{Input: request["input"]}).InputCount()
		if err != nil || count < 1 || count > frozen.Policy.MaxBatch {
			return knowledgeResponse{}, ErrInvalid
		}
		var envelope struct {
			Model string            `json:"model"`
			Data  []json.RawMessage `json:"data"`
		}
		if common.UnmarshalUnique(raw, &envelope) != nil || envelope.Model != upstream || len(envelope.Data) != count || frozen.Policy.Space == nil {
			return knowledgeResponse{}, ErrInvalid
		}
		ordered := make([]json.RawMessage, count)
		for _, item := range envelope.Data {
			var value struct {
				Index     *int              `json:"index"`
				Embedding []json.RawMessage `json:"embedding"`
			}
			if common.UnmarshalUnique(item, &value) != nil || value.Index == nil || *value.Index < 0 || *value.Index >= count || ordered[*value.Index] != nil || len(value.Embedding) != frozen.Policy.Space.Dimensions {
				return knowledgeResponse{}, ErrInvalid
			}
			for _, component := range value.Embedding {
				var n float64
				if bytes.Equal(bytes.TrimSpace(component), []byte("null")) || common.Unmarshal(component, &n) != nil || math.IsNaN(n) || math.IsInf(n, 0) {
					return knowledgeResponse{}, ErrInvalid
				}
			}
			ordered[*value.Index] = item
		}
		wire["data"], err = common.Marshal(ordered)
		if err != nil {
			return knowledgeResponse{}, ErrInvalid
		}
		result.body, err = common.Marshal(wire)
		if err != nil || len(result.body) > knowledgeResultMaxBytes {
			return knowledgeResponse{}, ErrInvalid
		}
		var usageFields map[string]json.RawMessage
		if common.UnmarshalUnique(wire["usage"], &usageFields) != nil || usageFields == nil {
			return knowledgeResponse{}, ErrInvalid
		}
		if completion, exists := usageFields["completion_tokens"]; exists {
			counter := bytes.TrimSpace(completion)
			if !bytes.Equal(counter, []byte("0")) {
				return knowledgeResponse{}, ErrInvalid
			}
		}
		source := dto.BillingUsageSourceOAIChat
		if frozen.Policy.Version == "knowledge_ali_text_v1" {
			source = dto.BillingUsageSourceAliEmbeddingV4
		}
		evidence := dto.NativeTokenEvidence{Source: source}
		evidence.Observe(raw, false)
		result.usage = evidence.Usage(*rate.Metered)
	case "rerank.create":
		var request dto.RerankRequest
		if common.UnmarshalUnique(frozen.Body, &request) != nil || len(request.Documents) == 0 {
			return knowledgeResponse{}, ErrInvalid
		}
		var envelope struct {
			Output struct {
				Results []json.RawMessage `json:"results"`
			} `json:"output"`
			Usage map[string]json.RawMessage `json:"usage"`
			Code  string                     `json:"code"`
		}
		if common.UnmarshalUnique(raw, &envelope) != nil || envelope.Code != "" || len(envelope.Usage) != 1 || envelope.Usage["total_tokens"] == nil {
			return knowledgeResponse{}, ErrInvalid
		}
		count := len(request.Documents)
		if request.TopN != nil {
			count = *request.TopN
		}
		if dto.ValidateRerankResults(envelope.Output.Results, len(request.Documents), count) != nil {
			return knowledgeResponse{}, ErrInvalid
		}
		for i, rawItem := range envelope.Output.Results {
			var item struct {
				Index    int `json:"index"`
				Document *struct {
					Text string `json:"text"`
				} `json:"document"`
			}
			if common.UnmarshalUnique(rawItem, &item) != nil {
				return knowledgeResponse{}, ErrInvalid
			}
			doc, ok := request.Documents[item.Index].(string)
			if !ok || (item.Document != nil && item.Document.Text != doc) || ((request.ReturnDocuments == nil || *request.ReturnDocuments) && item.Document == nil) {
				return knowledgeResponse{}, ErrInvalid
			}
			if request.ReturnDocuments != nil && !*request.ReturnDocuments {
				var fields map[string]json.RawMessage
				if common.UnmarshalUnique(rawItem, &fields) != nil {
					return knowledgeResponse{}, ErrInvalid
				}
				delete(fields, "document")
				envelope.Output.Results[i], err = common.Marshal(fields)
				if err != nil {
					return knowledgeResponse{}, ErrInvalid
				}
			}
		}
		var number json.Number
		counter := bytes.TrimSpace(envelope.Usage["total_tokens"])
		if len(counter) == 0 || counter[0] < '0' || counter[0] > '9' || common.Unmarshal(counter, &number) != nil {
			return knowledgeResponse{}, ErrInvalid
		}
		quantity, err := confirmedCount(number)
		if err != nil || int64(quantity) > row.ReservationQuantity {
			return knowledgeResponse{}, ErrInvalid
		}
		result.usage = metering.Usage{Version: "metered_v2", Operation: "rerank", State: "confirmed", Source: "ali_rerank_actual_v1", Measurements: []metering.Measurement{{Key: "input", Unit: "token", Quantity: metering.Quantity{Value: strconv.Itoa(quantity)}}}}
		usage := dto.Usage{PromptTokens: quantity, TotalTokens: quantity}
		if row.WireMode == "knowledge-native-v1" {
			result.body, err = common.Marshal(struct {
				Output struct {
					Results []json.RawMessage `json:"results"`
				} `json:"output"`
				Usage dto.Usage `json:"usage"`
			}{Output: struct {
				Results []json.RawMessage `json:"results"`
			}{envelope.Output.Results}, Usage: usage})
		} else if row.WireMode == "" {
			result.body, err = common.Marshal(struct {
				Results []json.RawMessage `json:"results"`
				Usage   dto.Usage         `json:"usage"`
			}{Results: envelope.Output.Results, Usage: usage})
		} else {
			return knowledgeResponse{}, ErrInvalid
		}
		if err != nil || len(result.body) > knowledgeResultMaxBytes {
			return knowledgeResponse{}, ErrInvalid
		}
	default:
		return knowledgeResponse{}, ErrInvalid
	}
	if metering.ValidateUsage(*rate.Metered, result.usage) != nil {
		return knowledgeResponse{}, ErrInvalid
	}
	sum := new(big.Int)
	for _, measurement := range result.usage.Measurements {
		quantity, ok := new(big.Int).SetString(measurement.Quantity.Value, 10)
		if !ok || quantity.Sign() < 0 || measurement.Quantity.Scale != 0 {
			return knowledgeResponse{}, ErrInvalid
		}
		sum.Add(sum, quantity)
	}
	if sum.Cmp(big.NewInt(row.ReservationQuantity)) > 0 {
		return knowledgeResponse{}, ErrInvalid
	}
	result.rawDigest, result.snapshot, result.attempt = sha256.Sum256(raw), row, verifiedAttempt
	return result, nil
}

func sameKnowledgeCallBinding(a, b Call) bool {
	return a.CallID == b.CallID && a.ActorID == b.ActorID && a.AccountID == b.AccountID && a.TaskID == b.TaskID && a.OriginProductID == b.OriginProductID && a.InstanceID == b.InstanceID && a.CredentialID == b.CredentialID && a.CredentialKind == b.CredentialKind && a.Model == b.Model && a.Digest == b.Digest && a.DigestVersion == b.DigestVersion && a.WireOperation == b.WireOperation && a.WirePathModel == b.WirePathModel && a.WireMode == b.WireMode && a.Operation == b.Operation && a.MediaSnapshot == b.MediaSnapshot && a.RateSnapshot == b.RateSnapshot && a.RateID == b.RateID && a.Asset == b.Asset && a.FundingMode == b.FundingMode && a.FundingProductID == b.FundingProductID && a.FundingSnapshot == b.FundingSnapshot && a.BillingProfileVersion == b.BillingProfileVersion && a.Amount == b.Amount && a.ReservationQuantity == b.ReservationQuantity && a.ReservationOutput == b.ReservationOutput
}
