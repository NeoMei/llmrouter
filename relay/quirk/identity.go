package quirk

import (
	"regexp"
	"strings"

	"github.com/QuantumNous/new-api/dto"
)

// ponytail: 移植自 opencodex identity.ts
// Codex 对每个模型都注入 GPT-5 身份提示，需要移除避免非 OpenAI 模型误报身份
var identityPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)you are gpt-?\d+`),
	regexp.MustCompile(`(?i)you are an ai assistant from openai`),
	regexp.MustCompile(`(?i)you are chatgpt`),
}

// NeutralizeIdentity 扫描 system/developer message，移除 identity 行。
// 只处理纯文本 content，避免把含图片的结构化 content 展平成纯文本导致丢数据。
func NeutralizeIdentity(req *dto.GeneralOpenAIRequest) {
	for i := range req.Messages {
		msg := &req.Messages[i]
		if msg.Role != "system" && msg.Role != "developer" {
			continue
		}
		if !msg.IsStringContent() {
			continue
		}
		lines := strings.Split(msg.StringContent(), "\n")
		kept := lines[:0]
		for _, line := range lines {
			if isIdentityLine(line) {
				continue
			}
			kept = append(kept, line)
		}
		req.Messages[i].SetStringContent(strings.Join(kept, "\n"))
	}
}

func isIdentityLine(line string) bool {
	for _, p := range identityPatterns {
		if p.MatchString(line) {
			return true
		}
	}
	return false
}
