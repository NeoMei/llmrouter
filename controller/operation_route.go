package controller

import (
	"slices"

	"github.com/QuantumNous/new-api/relay/capability"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/service/modelcatalog"
	"github.com/QuantumNous/new-api/types"
)

// operationOnlyCandidate uses the endpoint's fixed native protocol, without
// inventing a Chat wire for image, embedding, rerank or audio requests.
func operationOnlyCandidate(info *relaycommon.RelayInfo, resolved *modelcatalog.ResolvedModel) bool {
	if info == nil || resolved == nil || len(resolved.PreferredWires) != 0 || !modelcatalog.OperationOnlyRoute(resolved.Capabilities.Operations) {
		return false
	}
	var operation capability.Operation
	switch info.RelayFormat {
	case types.RelayFormatOpenAIImage:
		switch info.RelayMode {
		case relayconstant.RelayModeImagesGenerations:
			operation = capability.OperationImageGeneration
		case relayconstant.RelayModeImagesEdits:
			operation = capability.OperationImageEdit
		}
	case types.RelayFormatEmbedding:
		if info.RelayMode == relayconstant.RelayModeEmbeddings {
			operation = capability.OperationEmbeddingCreate
		}
	case types.RelayFormatMultimodalEmbedding:
		if info.RelayMode == relayconstant.RelayModeMultimodalEmbedding {
			operation = capability.OperationMultimodalEmbedding
		}
	case types.RelayFormatRerank:
		if info.RelayMode == relayconstant.RelayModeRerank {
			operation = capability.OperationRerankCreate
		}
	case types.RelayFormatOpenAIAudio:
		switch info.RelayMode {
		case relayconstant.RelayModeAudioSpeech:
			operation = capability.OperationAudioSpeech
		case relayconstant.RelayModeAudioTranscription:
			operation = capability.OperationAudioTranscription
		}
	case types.RelayFormatDeepSeekTools:
		switch info.RelayMode {
		case relayconstant.RelayModeToolsSearch:
			operation = capability.OperationToolsSearch
		case relayconstant.RelayModeToolsFetch:
			operation = capability.OperationToolsFetch
		}
	}
	return operation != "" && slices.Contains(resolved.Capabilities.Operations, string(operation))
}
