package onboarding

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type aiderAdapter struct{}

func (aiderAdapter) ID() ClientID { return ClientAider }

func (aiderAdapter) Discover(_ Platform, home string) Discovery {
	config := filepath.Join(home, ".aider.conf.yml")
	environment := filepath.Join(home, ".config", "llmrouter", "aider.env")
	_, err := exec.LookPath("aider")
	return Discovery{Installed: fileExists(config) || err == nil, Paths: []string{config, environment}, Automatic: true}
}

func (aiderAdapter) Plan(_ context.Context, request ConfigureRequest) (ConfigPlan, error) {
	if err := validateConfigureRequest(request); err != nil {
		return ConfigPlan{}, err
	}
	if request.Model == "" {
		return ConfigPlan{}, fmt.Errorf("%w: Aider requires a selected model", ErrUsage)
	}
	configPath := filepath.Join(request.Home, ".aider.conf.yml")
	environmentPath := filepath.Join(request.Home, ".config", "llmrouter", "aider.env")
	config, _, err := readPlannedFile(request.Home, configPath)
	if err != nil {
		return ConfigPlan{}, err
	}
	var document yaml.Node
	if strings.TrimSpace(string(config)) != "" {
		if err := yaml.Unmarshal(config, &document); err != nil || len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
			return ConfigPlan{}, fmt.Errorf("%w: invalid Aider YAML", ErrUsage)
		}
	} else {
		document = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	}
	stripAiderMarkerComments(&document)
	root := document.Content[0]
	environmentIndex, err := setAiderScalar(root, "env-file", environmentPath)
	if err != nil {
		return ConfigPlan{}, fmt.Errorf("%w: invalid Aider managed keys", ErrConflict)
	}
	modelIndex, err := setAiderScalar(root, "model", "openai/"+request.Model)
	if err != nil {
		return ConfigPlan{}, fmt.Errorf("%w: invalid Aider managed keys", ErrConflict)
	}
	first, last := environmentIndex, modelIndex
	if first > last {
		first, last = last, first
	}
	root.Content[first].HeadComment = appendYAMLComment(root.Content[first].HeadComment, "LLMROUTER:BEGIN aider")
	root.Content[last+1].FootComment = appendYAMLComment(root.Content[last+1].FootComment, "LLMROUTER:END aider")
	configured, err := yaml.Marshal(&document)
	if err != nil {
		return ConfigPlan{}, fmt.Errorf("%w: encode Aider YAML", ErrConflict)
	}
	if err := validateAiderGeneratedConfig(configured, environmentPath, request.Model); err != nil {
		return ConfigPlan{}, fmt.Errorf("%w: generated Aider YAML is invalid", ErrConflict)
	}
	environment, _, err := readPlannedFile(request.Home, environmentPath)
	if err != nil {
		return ConfigPlan{}, err
	}
	environmentBlock := strings.Join([]string{
		"OPENAI_API_BASE=" + strconv.Quote(endpointV1(request.ServerURL)),
		"OPENAI_API_KEY=" + strconv.Quote(request.Token.Reveal()),
	}, "\n")
	configuredEnvironment, err := ApplyManagedBlock(string(environment), "aider-env", environmentBlock)
	if err != nil {
		return ConfigPlan{}, err
	}
	return ConfigPlan{Client: ClientAider, Writes: []PlannedWrite{
		{Path: configPath, Data: configured, Mode: privateFileMode},
		{Path: environmentPath, Data: []byte(configuredEnvironment), Mode: privateFileMode},
	}, RestartRequired: true}, nil
}

func setAiderScalar(mapping *yaml.Node, key, value string) (int, error) {
	index := -1
	for current := 0; current+1 < len(mapping.Content); current += 2 {
		if mapping.Content[current].Kind != yaml.ScalarNode || mapping.Content[current].Value != key {
			continue
		}
		if index >= 0 {
			return -1, fmt.Errorf("duplicate %s", key)
		}
		index = current
	}
	if index < 0 {
		mapping.Content = append(mapping.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value},
		)
		return len(mapping.Content) - 2, nil
	}
	existing := mapping.Content[index+1]
	mapping.Content[index+1] = &yaml.Node{
		Kind: yaml.ScalarNode, Tag: "!!str", Value: value,
		Style: existing.Style, Anchor: existing.Anchor,
		HeadComment: existing.HeadComment, LineComment: existing.LineComment, FootComment: existing.FootComment,
	}
	return index, nil
}

func stripAiderMarkerComments(node *yaml.Node) {
	if node == nil {
		return
	}
	node.HeadComment = withoutExactYAMLCommentLines(node.HeadComment, "LLMROUTER:BEGIN aider", "LLMROUTER:END aider")
	node.LineComment = withoutExactYAMLCommentLines(node.LineComment, "LLMROUTER:BEGIN aider", "LLMROUTER:END aider")
	node.FootComment = withoutExactYAMLCommentLines(node.FootComment, "LLMROUTER:BEGIN aider", "LLMROUTER:END aider")
	for _, child := range node.Content {
		stripAiderMarkerComments(child)
	}
}

func withoutExactYAMLCommentLines(comment string, markers ...string) string {
	lines := strings.Split(comment, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		remove := false
		for _, marker := range markers {
			if line == marker || line == "# "+marker {
				remove = true
				break
			}
		}
		if !remove {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

func appendYAMLComment(comment, line string) string {
	if comment == "" {
		return line
	}
	return comment + "\n" + line
}

func validateAiderGeneratedConfig(data []byte, environmentPath, model string) error {
	var root map[string]any
	if err := yaml.Unmarshal(data, &root); err != nil || root == nil {
		return fmt.Errorf("invalid YAML")
	}
	if root["env-file"] != environmentPath || root["model"] != "openai/"+model {
		return fmt.Errorf("managed values mismatch")
	}
	return nil
}

func (aiderAdapter) Check(_ context.Context, request CheckRequest) []Check {
	config := filepath.Join(request.Home, ".aider.conf.yml")
	environment := filepath.Join(request.Home, ".config", "llmrouter", "aider.env")
	return []Check{
		semanticConfigCheck("aider-config", checkAiderConfig(request, config, environment)),
		semanticConfigCheck("aider-environment", checkAiderEnvironment(request, environment)),
	}
}

func checkAiderConfig(request CheckRequest, configPath, environmentPath string) error {
	if request.Model == "" {
		return fmt.Errorf("expected Aider model is missing")
	}
	data, err := loadManagedConfig(request.Home, configPath)
	if err != nil {
		return err
	}
	var root map[string]any
	if err := yaml.Unmarshal(data, &root); err != nil || root == nil {
		return fmt.Errorf("invalid Aider YAML")
	}
	if root["env-file"] != environmentPath || root["model"] != "openai/"+request.Model {
		return fmt.Errorf("Aider model or environment mismatch")
	}
	return nil
}

func checkAiderEnvironment(request CheckRequest, environmentPath string) error {
	if request.ServerURL == nil || request.Token.Reveal() == "" {
		return fmt.Errorf("expected Aider environment is incomplete")
	}
	data, err := loadManagedConfig(request.Home, environmentPath)
	if err != nil {
		return err
	}
	managed, err := aiderManagedEnvironment(string(data))
	if err != nil {
		return err
	}
	values := make(map[string]string)
	for _, line := range strings.Split(managed, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, raw, ok := strings.Cut(line, "=")
		if !ok || key == "" {
			return fmt.Errorf("invalid Aider environment")
		}
		value, err := strconv.Unquote(raw)
		if err != nil {
			return fmt.Errorf("invalid Aider environment value")
		}
		values[key] = value
	}
	if len(values) != 2 || values["OPENAI_API_BASE"] != endpointV1(request.ServerURL) || values["OPENAI_API_KEY"] != request.Token.Reveal() {
		return fmt.Errorf("Aider environment mismatch")
	}
	return nil
}

func aiderManagedEnvironment(input string) (string, error) {
	begin := "# LLMROUTER:BEGIN aider-env"
	end := "# LLMROUTER:END aider-env"
	inBlock := false
	found := false
	var content strings.Builder
	for _, line := range strings.SplitAfter(input, "\n") {
		text := strings.TrimSuffix(line, "\n")
		switch text {
		case begin:
			if inBlock || found {
				return "", fmt.Errorf("duplicate Aider environment block")
			}
			inBlock, found = true, true
		case end:
			if !inBlock {
				return "", fmt.Errorf("unmatched Aider environment block")
			}
			inBlock = false
		default:
			if inBlock {
				content.WriteString(line)
			}
		}
	}
	if inBlock || !found {
		return "", fmt.Errorf("Aider environment block is unavailable")
	}
	return content.String(), nil
}

var _ ClientAdapter = aiderAdapter{}
