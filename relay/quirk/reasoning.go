package quirk

import (
	"github.com/QuantumNous/new-api/dto"
)

// ponytail: 移植自 opencodex bridge.ts
//
// 国产模型（DeepSeek/Kimi/Qwen 等）把思考过程放在 delta.reasoning_content
// （非标准字段）。opencodex bridge.ts 需要手动将其转为 Responses API 的
// reasoning output item SSE 序列。
//
// 在 new-api 中，relayconvert 管线已经完整覆盖了这个转换：
//
//   - reasoning_content → Responses reasoning output items
//     service/relayconvert/internal/oai_chat/to_oai_responses_stream_resp.go
//     ChatCompletionsStreamChunkToResponsesEvents → appendReasoningDelta
//     通过 delta.GetReasoningContent() 同时兼容 reasoning_content 和 reasoning 字段。
//
//   - streaming tool call 分片累积 → function_call output items
//     同文件 appendToolCallDelta，按 chatIndex 累积 name/arguments，
//     finish_reason 时 doneDeltaEvents 原子输出。
//
// OaiChatToResponsesStreamHandler (relay/channel/openai/responses_via_chat.go)
// 通过 relayconvert.ConvertStreamResponseChunk 调用上述逻辑，
// 产出完整的 Responses SSE 事件序列（output_item.added → *.delta → *.done）。
//
// 因此本包不重复实现 ResponsesStreamHandler —— relayconvert 的实现更完整
// （正确的事件类型、output_index 管理、usage 跟踪、错误处理），
// 重新实现只会产出一个不兼容的并行管线。
//
// ToolCallBuffer 作为独立工具保留，供 relayconvert 管线之外、需要手动
// 累积 streaming tool call 的路径使用（如自定义 adapter、测试辅助等）。

// ChunkHasReasoningContent 判断一个 streaming chunk 是否携带 reasoning_content，
// 用于诊断/日志（国产模型思考链路是否激活）。
func ChunkHasReasoningContent(chunk *dto.ChatCompletionsStreamResponse) bool {
	if chunk == nil || len(chunk.Choices) == 0 {
		return false
	}
	return chunk.Choices[0].Delta.GetReasoningContent() != ""
}
