package modelcatalog

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/capability"
)

// NormalizeModelCapabilities is the shared configuration boundary used by the
// catalog, runtime assembly and management validation.
func NormalizeModelCapabilities(raw string) (dto.ModelCapabilities, error) {
	metadata, err := decodeRouteMetadata(raw)
	return metadata.ModelCapabilities, err
}

func normalizeCapabilities(m *dto.ModelCapabilities) error {
	for _, values := range []*[]string{&m.InputModalities, &m.OutputModalities, &m.Operations} {
		if *values == nil {
			continue
		}
		if len(*values) == 0 {
			return fmt.Errorf("declared capability list must not be empty")
		}
		normalized := make([]string, 0, len(*values))
		seen := map[string]bool{}
		for _, v := range *values {
			v = strings.ToLower(strings.TrimSpace(v))
			if v == "" {
				return fmt.Errorf("capability must not be blank")
			}
			if !seen[v] {
				normalized = append(normalized, v)
				seen[v] = true
			}
		}
		*values = normalized
	}
	for _, values := range [][]string{m.InputModalities, m.OutputModalities} {
		for _, v := range values {
			switch v {
			case "text", "image", "audio", "video", "document", "embedding":
			default:
				return fmt.Errorf("unknown modality %q", v)
			}
		}
	}
	known := map[string]bool{}
	for _, op := range capability.AllOperations() {
		known[string(op)] = true
	}
	for _, op := range m.Operations {
		if !known[op] || strings.HasPrefix(op, "models.") {
			return fmt.Errorf("unsupported model operation %q", op)
		}
	}
	if l := m.MediaLimits; l != nil {
		for name, v := range map[string]*int{"max_images": l.MaxImages, "max_pages": l.MaxPages} {
			if v != nil && *v <= 0 {
				return fmt.Errorf("%s must be positive", name)
			}
		}
		for name, v := range map[string]*int64{"max_request_bytes": l.MaxRequestBytes, "max_pixels": l.MaxPixels} {
			if v != nil && *v <= 0 {
				return fmt.Errorf("%s must be positive", name)
			}
		}
		for name, v := range map[string]*float64{"max_audio_seconds": l.MaxAudioSeconds, "max_video_seconds": l.MaxVideoSeconds} {
			if v != nil && (*v <= 0 || math.IsInf(*v, 0) || math.IsNaN(*v)) {
				return fmt.Errorf("%s must be finite and positive", name)
			}
		}
		for i, v := range l.Formats {
			v = strings.ToLower(strings.TrimSpace(v))
			if v == "" {
				return fmt.Errorf("media format must not be blank")
			}
			l.Formats[i] = v
		}
	}
	if s := m.EmbeddingSpace; s != nil {
		s.Provider = strings.TrimSpace(s.Provider)
		s.Model = strings.TrimSpace(s.Model)
		s.Version = strings.TrimSpace(s.Version)
		s.InputSemantics = strings.TrimSpace(s.InputSemantics)
		if s.Provider == "" || s.Model == "" || s.Version == "" || s.InputSemantics == "" || s.Dimensions <= 0 || s.Dimensions > dto.MaxEmbeddingDimensions {
			return fmt.Errorf("embedding_space requires provider, model, version, positive bounded dimensions and input_semantics")
		}
	}
	return nil
}

func validateEmbeddingTarget(space *dto.EmbeddingSpace, target model.ClientModelTarget) error {
	if strings.TrimSpace(target.Metadata) != "" {
		var fields map[string]json.RawMessage
		if err := common.UnmarshalUnique([]byte(target.Metadata), &fields); err != nil {
			return err
		}
		for name := range fields {
			if name != "embedding_space" {
				return fmt.Errorf("target %d only supports embedding_space metadata; use route metadata for %s", target.Id, name)
			}
		}
	}
	declared, err := NormalizeModelCapabilities(target.Metadata)
	if err != nil {
		return fmt.Errorf("target %d metadata: %w", target.Id, err)
	}
	if space == nil {
		if declared.EmbeddingSpace != nil {
			return fmt.Errorf("target %d embedding_space requires route embedding_space", target.Id)
		}
		return nil
	}
	if target.UpstreamModel != space.Model {
		return fmt.Errorf("target %d upstream model differs from fixed embedding_space", target.Id)
	}
	if declared.EmbeddingSpace != nil && *declared.EmbeddingSpace != *space {
		return fmt.Errorf("target %d embedding_space differs from route", target.Id)
	}
	return nil
}

func cloneCapabilities(m dto.ModelCapabilities) dto.ModelCapabilities {
	// All fields are JSON values; deep copy keeps the immutable catalog snapshot
	// isolated from callers including nested pointer scalars.
	copy, err := common.DeepCopy(&m)
	if err != nil {
		return dto.ModelCapabilities{}
	}
	return *copy
}

// ValidateChannelOperations prevents a route from advertising operations its
// configured adapter cannot execute. An absent list retains old configuration.
func ValidateChannelOperations(m dto.ModelCapabilities, channelType int) error {
	for _, name := range m.Operations {
		op := capability.Operation(name)
		if !capability.DefaultRegistry().Supports(channelType, capability.ProtocolForOperation(op), op) {
			return fmt.Errorf("channel does not support operation %s", name)
		}
	}
	return nil
}
