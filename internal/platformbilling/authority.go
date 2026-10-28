package platformbilling

import (
	"context"
	"slices"
)

// WireOperation is supplied by the fixed server route, never by a request's
// billing selector. PathModel and Mode bind native Gemini's path and stream mode.
type WireOperation struct{ ID, PathModel, Mode string }

func wireBillingOperation(wire string) string {
	switch wire {
	case "chat.completions.create", "claude.messages.create", "gemini.generate":
		return "chat"
	case "responses.create", "responses.compact":
		return "responses"
	case "rerank.create":
		return "rerank"
	case "multimodal.embeddings.create":
		return "multimodal_embedding"
	case "embeddings.create":
		return "embedding"
	case "audio.speech.create", "bailian.audio.speech.create", "elevenlabs.audio.speech.create":
		return "speech"
	case "audio.transcriptions.create", "dashscope.audio.transcriptions.create":
		return "transcription"
	case "images.generations.create", "dashscope.images.generate", "gemini.images.generate":
		return "image_generation"
	case "images.edits.create":
		return "image_edit"
	case "videos.create":
		return "video"
	case "ocr.jobs.create", "dashscope.ocr.recognitions.create":
		return "ocr"
	}
	return ""
}
func legacyWireOperation(op string) string {
	switch op {
	case "chat":
		return "chat.completions.create"
	case "responses":
		return "responses.create"
	case "rerank":
		return "rerank.create"
	case "multimodal_embedding":
		return "multimodal.embeddings.create"
	case "embedding":
		return "embeddings.create"
	case "speech":
		return "audio.speech.create"
	case "transcription":
		return "audio.transcriptions.create"
	case "image_generation":
		return "images.generations.create"
	case "image_edit":
		return "images.edits.create"
	case "video":
		return "videos.create"
	case "ocr":
		return "ocr.jobs.create"
	}
	return ""
}
func allowsWire(grant Claims, model, wire string) bool {
	if wireBillingOperation(wire) == "" {
		return false
	}

	return slices.Contains(grant.Operations[model], wire)
}

func (a *Adapter) AdmitWireOperation(ctx context.Context, credential, task, id string, wire WireOperation, contentType string, body []byte) (*Session, error) {
	return a.admitWireOperation(ctx, credential, task, id, wire, contentType, body)
}

// Compare the legacy digest only for the historical managed wire. Never rewrite
// an old row or reinterpret a Chat call as a newly introduced native protocol.
func compareWireCall(old, row Call, legacyDigest string) error {
	operation := old.Operation
	if operation == "" {
		operation = "chat"
	}
	if old.DigestVersion == "" && old.WireOperation == "" && row.WireOperation == legacyWireOperation(operation) && row.WirePathModel == "" && row.WireMode == "" {
		row.WireOperation = ""
		row.DigestVersion = ""
		row.Digest = legacyDigest
	}
	return compareCall(old, row)
}
