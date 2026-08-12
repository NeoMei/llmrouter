package onboarding

import (
	"fmt"
	"net/url"
)

func ManualSnippet(kind string, server *url.URL, model string) (string, error) {
	if server == nil || model == "" || hasControl(model) {
		return "", fmt.Errorf("%w: server URL and model are required", ErrUsage)
	}
	if _, err := NormalizeServerURL(server.String()); err != nil {
		return "", err
	}
	base := endpointV1(server)
	switch kind {
	case "cursor", "cline", "roo-code":
		return fmt.Sprintf("Provider: OpenAI Compatible\nBase URL: %s\nAPI key: ${LLMROUTER_TOKEN}\nModel: %s", base, model), nil
	case "openai-python":
		return fmt.Sprintf("from openai import OpenAI\nimport os\nclient = OpenAI(base_url=%q, api_key=os.environ[\"LLMROUTER_TOKEN\"])\nclient.chat.completions.create(model=%q, messages=[{\"role\": \"user\", \"content\": \"Hello\"}], max_tokens=16)", base, model), nil
	case "openai-node":
		return fmt.Sprintf("import OpenAI from 'openai';\nconst client = new OpenAI({baseURL: %q, apiKey: process.env.LLMROUTER_TOKEN});\nawait client.chat.completions.create({model: %q, messages: [{role: 'user', content: 'Hello'}], max_tokens: 16});", base, model), nil
	case "openai-sdk":
		python, _ := ManualSnippet("openai-python", server, model)
		node, _ := ManualSnippet("openai-node", server, model)
		return "Python:\n" + python + "\n\nNode:\n" + node, nil
	case "langchain":
		return fmt.Sprintf("ChatOpenAI(model=%q, base_url=%q, api_key=os.environ[\"LLMROUTER_TOKEN\"], max_tokens=16)", model, base), nil
	default:
		return "", fmt.Errorf("%w: unknown manual client %q", ErrUsage, kind)
	}
}
