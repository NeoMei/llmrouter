package onboarding

import (
	"fmt"
	"strings"
)

func ApplyManagedBlock(input, client, content string) (string, error) {
	if hasControl(client) || client == "" || strings.ContainsAny(client, " \t") {
		return "", fmt.Errorf("%w: invalid client marker", ErrUsage)
	}
	without, err := RemoveManagedBlock(input, client)
	if err != nil {
		return "", err
	}
	content = strings.TrimSuffix(content, "\n")
	block := "# LLMROUTER:BEGIN " + client + "\n" + content + "\n# LLMROUTER:END " + client + "\n"
	if without == "" {
		return block, nil
	}
	if !strings.HasSuffix(without, "\n") {
		without += "\n"
	}
	return without + block, nil
}

func RemoveManagedBlock(input, client string) (string, error) {
	begin := "# LLMROUTER:BEGIN " + client
	end := "# LLMROUTER:END " + client
	lines := strings.SplitAfter(input, "\n")
	var output strings.Builder
	inBlock := false
	found := false
	for _, line := range lines {
		text := strings.TrimSuffix(line, "\n")
		if text == begin {
			if inBlock || found {
				return "", fmt.Errorf("%w: duplicate managed block for %s", ErrConflict, client)
			}
			inBlock, found = true, true
			continue
		}
		if text == end {
			if !inBlock {
				return "", fmt.Errorf("%w: unmatched managed block end for %s", ErrConflict, client)
			}
			inBlock = false
			continue
		}
		if inBlock {
			continue
		}
		output.WriteString(line)
	}
	if inBlock {
		return "", fmt.Errorf("%w: unterminated managed block for %s", ErrConflict, client)
	}
	return output.String(), nil
}
