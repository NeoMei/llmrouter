package controller

import (
	"testing"

	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/service/modelcatalog"
	"github.com/QuantumNous/new-api/types"
	"github.com/stretchr/testify/require"
)

func TestOperationOnlyCandidateKeepsFixedRequestProtocol(t *testing.T) {
	for _, tc := range []struct {
		format    types.RelayFormat
		mode      int
		operation string
	}{
		{types.RelayFormatOpenAIImage, relayconstant.RelayModeImagesGenerations, "images.generations.create"},
		{types.RelayFormatOpenAIImage, relayconstant.RelayModeImagesEdits, "images.edits.create"},
		{types.RelayFormatEmbedding, relayconstant.RelayModeEmbeddings, "embeddings.create"},
		{types.RelayFormatMultimodalEmbedding, relayconstant.RelayModeMultimodalEmbedding, "multimodal.embeddings.create"},
		{types.RelayFormatRerank, relayconstant.RelayModeRerank, "rerank.create"},
		{types.RelayFormatOpenAIAudio, relayconstant.RelayModeAudioSpeech, "audio.speech.create"},
		{types.RelayFormatOpenAIAudio, relayconstant.RelayModeAudioTranscription, "audio.transcriptions.create"},
	} {
		t.Run(tc.operation, func(t *testing.T) {
			resolved := &modelcatalog.ResolvedModel{Capabilities: dto.ModelCapabilities{Operations: []string{tc.operation}}}
			info := &relaycommon.RelayInfo{RelayFormat: tc.format, RelayMode: tc.mode}
			decision, err := selectWireForRequest(info, resolved)
			require.NoError(t, err)
			require.Empty(t, decision.Wire, "fixed operation endpoints must not fabricate a conversational wire")
			require.Equal(t, "declared_operation", decision.Source)
			_, err = selectWireForRequest(&relaycommon.RelayInfo{RelayFormat: types.RelayFormatOpenAI, RelayMode: relayconstant.RelayModeChatCompletions}, resolved)
			require.Error(t, err)
			info.RelayMode = relayconstant.RelayModeChatCompletions
			_, err = selectWireForRequest(info, resolved)
			require.Error(t, err)
		})
	}
}
