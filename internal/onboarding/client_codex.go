package onboarding

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
)

type codexAdapter struct{ id ClientID }

func (adapter codexAdapter) ID() ClientID { return adapter.id }

func (adapter codexAdapter) Discover(platform Platform, home string) Discovery {
	path := filepath.Join(home, ".codex", "config.toml")
	_, binaryErr := exec.LookPath("codex")
	installed := fileExists(path) || binaryErr == nil
	if adapter.id == ClientCodexDesktop {
		installed = installed || (platform.OS == "darwin" && fileExists("/Applications/Codex.app/Contents/MacOS/Codex"))
	}
	return Discovery{Installed: installed, Paths: []string{path}, Automatic: true}
}

func (adapter codexAdapter) Plan(_ context.Context, request ConfigureRequest) (ConfigPlan, error) {
	if err := validateConfigureRequest(request); err != nil {
		return ConfigPlan{}, err
	}
	if request.Model == "" {
		return ConfigPlan{}, fmt.Errorf("%w: Codex requires a selected model", ErrUsage)
	}
	path := filepath.Join(request.Home, ".codex", "config.toml")
	existing, _, err := readPlannedFile(request.Home, path)
	if err != nil {
		return ConfigPlan{}, err
	}
	if len(existing) != 0 {
		var root map[string]any
		if err := toml.Unmarshal(existing, &root); err != nil {
			return ConfigPlan{}, fmt.Errorf("%w: invalid Codex TOML", ErrUsage)
		}
	}
	configured, err := mergeCodexText(string(existing), request)
	if err != nil {
		return ConfigPlan{}, fmt.Errorf("%w: inspect Codex TOML expressions", ErrConflict)
	}
	if err := validateConfigSyntax(path, []byte(configured)); err != nil {
		return ConfigPlan{}, fmt.Errorf("%w: Codex configuration conflicts with the managed provider", ErrConflict)
	}
	instructions := []string{"Set LLMAPI_TOKEN in the environment before launching Codex."}
	if adapter.id == ClientCodexDesktop {
		instructions = []string{"Set LLMAPI_TOKEN in the desktop launch environment, then restart Codex Desktop."}
	}
	return ConfigPlan{Client: adapter.id, Writes: []PlannedWrite{{Path: path, Data: []byte(configured), Mode: privateFileMode}}, Instructions: instructions, RestartRequired: adapter.id == ClientCodexDesktop}, nil
}

func mergeCodexText(input string, request ConfigureRequest) (string, error) {
	expressions, err := parseCodexExpressions([]byte(input))
	if err != nil {
		return "", err
	}
	rootInsertion := len(input)
	inRoot := true
	removals := make([]codexRange, 0, 6)
	for index, expression := range expressions {
		if expression.kind == unstable.Table || expression.kind == unstable.ArrayTable {
			if rootInsertion == len(input) {
				rootInsertion = expression.start
			}
			inRoot = false
		}
		if inRoot && expression.kind == unstable.KeyValue && len(expression.keys) == 1 && (expression.keys[0] == "model" || expression.keys[0] == "model_provider") {
			removals = append(removals, codexRange{start: expression.start, end: expression.end})
		}
		if expression.kind == unstable.Comment && isCodexMarker(expression.comment) {
			removals = append(removals, codexRange{start: expression.start, end: expression.end})
		}
		if expression.kind == unstable.Table && equalCodexKey(expression.keys, "model_providers", "llmapi") {
			removals = append(removals, codexRange{start: expression.start, end: expression.end})
			for next := index + 1; next < len(expressions); next++ {
				if expressions[next].kind == unstable.Table || expressions[next].kind == unstable.ArrayTable {
					break
				}
				if expressions[next].kind == unstable.KeyValue {
					removals = append(removals, codexRange{start: expressions[next].start, end: expressions[next].end})
				}
			}
		}
	}
	prefix := removeCodexRanges(input[:rootInsertion], clipCodexRanges(removals, 0, rootInsertion))
	suffix := removeCodexRanges(input[rootInsertion:], clipCodexRanges(removals, rootInsertion, len(input)))
	var output strings.Builder
	output.WriteString(prefix)
	ensureCodexNewline(&output)
	output.WriteString("# LLMAPI:BEGIN codex\n")
	output.WriteString("model = " + strconv.Quote(request.Model) + "\n")
	output.WriteString("model_provider = \"llmapi\"\n")
	output.WriteString("# LLMAPI:END codex\n")
	output.WriteString(suffix)
	ensureCodexNewline(&output)
	if !strings.HasSuffix(output.String(), "\n\n") {
		output.WriteByte('\n')
	}
	output.WriteString("[model_providers.llmapi]\n")
	output.WriteString("name = \"LLMAPI\"\n")
	output.WriteString("base_url = " + strconv.Quote(endpointV1(request.ServerURL)) + "\n")
	output.WriteString("wire_api = \"responses\"\n")
	output.WriteString("env_key = \"LLMAPI_TOKEN\"\n")
	return output.String(), nil
}

type codexExpression struct {
	kind       unstable.Kind
	keys       []string
	comment    string
	start, end int
}

type codexRange struct{ start, end int }

func parseCodexExpressions(data []byte) ([]codexExpression, error) {
	var parser unstable.Parser
	parser.KeepComments = true
	parser.Reset(data)
	expressions := make([]codexExpression, 0)
	for parser.NextExpression() {
		node := parser.Expression()
		start, end := codexNodeRange(node)
		if start < 0 {
			continue
		}
		for start > 0 && data[start-1] != '\n' {
			start--
		}
		for end < len(data) && data[end] != '\n' {
			end++
		}
		if end < len(data) {
			end++
		}
		expression := codexExpression{kind: node.Kind, start: start, end: end}
		if node.Kind == unstable.KeyValue || node.Kind == unstable.Table || node.Kind == unstable.ArrayTable {
			iterator := node.Key()
			for iterator.Next() {
				key := iterator.Node()
				if key.Kind != unstable.Key {
					break
				}
				expression.keys = append(expression.keys, string(key.Data))
			}
		}
		if node.Kind == unstable.Comment {
			expression.comment = string(node.Data)
		}
		expressions = append(expressions, expression)
	}
	if err := parser.Error(); err != nil {
		return nil, err
	}
	return expressions, nil
}

func codexNodeRange(node *unstable.Node) (int, int) {
	start, end := -1, -1
	var visit func(*unstable.Node)
	visit = func(current *unstable.Node) {
		for current != nil {
			if current.Raw.Length != 0 {
				nodeStart := int(current.Raw.Offset)
				nodeEnd := nodeStart + int(current.Raw.Length)
				if start < 0 || nodeStart < start {
					start = nodeStart
				}
				if nodeEnd > end {
					end = nodeEnd
				}
			}
			visit(current.Child())
			current = current.Next()
		}
	}
	visit(node)
	return start, end
}

func clipCodexRanges(ranges []codexRange, start, end int) []codexRange {
	clipped := make([]codexRange, 0, len(ranges))
	for _, item := range ranges {
		left, right := item.start, item.end
		if left < start {
			left = start
		}
		if right > end {
			right = end
		}
		if left < right {
			clipped = append(clipped, codexRange{start: left - start, end: right - start})
		}
	}
	return clipped
}

func removeCodexRanges(input string, ranges []codexRange) string {
	sort.Slice(ranges, func(i, j int) bool { return ranges[i].start < ranges[j].start })
	var output strings.Builder
	position := 0
	for _, item := range ranges {
		if item.start < position {
			if item.end > position {
				position = item.end
			}
			continue
		}
		output.WriteString(input[position:item.start])
		position = item.end
	}
	output.WriteString(input[position:])
	return output.String()
}

func ensureCodexNewline(output *strings.Builder) {
	if output.Len() != 0 && !strings.HasSuffix(output.String(), "\n") {
		output.WriteByte('\n')
	}
}

func equalCodexKey(keys []string, expected ...string) bool {
	if len(keys) != len(expected) {
		return false
	}
	for index := range keys {
		if keys[index] != expected[index] {
			return false
		}
	}
	return true
}

func isCodexMarker(comment string) bool {
	switch strings.TrimSpace(comment) {
	case "# LLMAPI:BEGIN codex", "# LLMAPI:END codex", "# Managed keys: model, model_provider, model_providers.llmapi":
		return true
	default:
		return false
	}
}

func (adapter codexAdapter) Check(_ context.Context, request CheckRequest) []Check {
	name := "codex-cli-config"
	if adapter.id == ClientCodexDesktop {
		name = "codex-desktop-config"
	}
	path := filepath.Join(request.Home, ".codex", "config.toml")
	check := semanticConfigCheck(name, checkCodexConfig(request, path))
	credential := Check{Name: "codex-cli-credential", Status: "FAIL", Message: "LLMAPI_TOKEN is not visible to the Codex CLI process"}
	if adapter.id == ClientCodexDesktop {
		credential = Check{Name: "codex-desktop-credential", Status: "SKIP", Message: "desktop launch credential visibility requires application-level verification"}
	} else if value, ok := os.LookupEnv("LLMAPI_TOKEN"); ok && value != "" && value == request.Token.Reveal() {
		credential = Check{Name: "codex-cli-credential", Status: "PASS", Message: "LLMAPI_TOKEN is visible to the Codex CLI process"}
	}
	return []Check{check, credential}
}

func checkCodexConfig(request CheckRequest, path string) error {
	if request.ServerURL == nil || request.Model == "" {
		return fmt.Errorf("expected Codex settings are incomplete")
	}
	data, err := loadManagedConfig(request.Home, path)
	if err != nil {
		return err
	}
	var root map[string]any
	if err := toml.Unmarshal(data, &root); err != nil {
		return err
	}
	if root["model_provider"] != "llmapi" || root["model"] != request.Model {
		return fmt.Errorf("Codex selection mismatch")
	}
	providers, ok := root["model_providers"].(map[string]any)
	if !ok {
		return fmt.Errorf("Codex providers missing")
	}
	provider, ok := providers["llmapi"].(map[string]any)
	if !ok || provider["name"] != "LLMAPI" || provider["base_url"] != endpointV1(request.ServerURL) || provider["wire_api"] != "responses" || provider["env_key"] != "LLMAPI_TOKEN" {
		return fmt.Errorf("Codex provider mismatch")
	}
	return nil
}

var _ ClientAdapter = codexAdapter{}
