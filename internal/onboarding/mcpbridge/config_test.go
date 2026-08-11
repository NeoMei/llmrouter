package mcpbridge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const testBridgeSHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestConfigRequiresTrustedBridgeChecksum(t *testing.T) {
	config := Config{
		Origin: "https://stage.example", Token: "secret", Models: []string{"model-a"}, TimeoutSeconds: 30,
		BridgeSHA256: strings.Repeat("a", 64),
	}
	require.NoError(t, config.Validate())
	config.BridgeSHA256 = ""
	require.Error(t, config.Validate())
	config.BridgeSHA256 = strings.Repeat("z", 64)
	require.Error(t, config.Validate())
}

func TestLoadConfigRequiresPrivateRegularFileAndValidBounds(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "claude-desktop.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"origin":"https://stage.example","token":"secret","models":["model-a"],"timeout_seconds":30,"bridge_sha256":"`+testBridgeSHA256+`"}`), 0o600))
	config, err := LoadConfig(path)
	require.NoError(t, err)
	require.Equal(t, "https://stage.example", config.Origin)
	require.Equal(t, []string{"model-a"}, config.Models)
	require.Equal(t, 30, config.TimeoutSeconds)

	require.NoError(t, os.Chmod(path, 0o644))
	_, err = LoadConfig(path)
	require.Error(t, err)
}

func TestLoadConfigRejectsSymlinkUnknownFieldsAndUnsafeValues(t *testing.T) {
	dir := t.TempDir()
	realPath := filepath.Join(dir, "real.json")
	require.NoError(t, os.WriteFile(realPath, []byte(`{"origin":"https://stage.example","token":"secret","models":["model-a"],"timeout_seconds":30,"bridge_sha256":"`+testBridgeSHA256+`"}`), 0o600))
	linkPath := filepath.Join(dir, "link.json")
	require.NoError(t, os.Symlink(realPath, linkPath))
	_, err := LoadConfig(linkPath)
	require.Error(t, err)

	tests := []string{
		`{"origin":"http://stage.example","token":"secret","models":["model-a"],"timeout_seconds":30,"bridge_sha256":"` + testBridgeSHA256 + `"}`,
		`{"origin":"https://stage.example/path","token":"secret","models":["model-a"],"timeout_seconds":30,"bridge_sha256":"` + testBridgeSHA256 + `"}`,
		`{"origin":"https://stage.example","token":"secret\nleak","models":["model-a"],"timeout_seconds":30,"bridge_sha256":"` + testBridgeSHA256 + `"}`,
		`{"origin":"https://stage.example","token":"secret","models":[],"timeout_seconds":30,"bridge_sha256":"` + testBridgeSHA256 + `"}`,
		`{"origin":"https://stage.example","token":"secret","models":["model-a","model-a"],"timeout_seconds":30,"bridge_sha256":"` + testBridgeSHA256 + `"}`,
		`{"origin":"https://stage.example","token":"secret","models":["model-a"],"timeout_seconds":31,"bridge_sha256":"` + testBridgeSHA256 + `"}`,
		`{"origin":"https://stage.example","token":"secret","models":["model-a"],"timeout_seconds":30,"bridge_sha256":"` + testBridgeSHA256 + `","url":"https://elsewhere.example"}`,
	}
	for index, input := range tests {
		path := filepath.Join(dir, "invalid-"+string(rune('a'+index))+".json")
		require.NoError(t, os.WriteFile(path, []byte(input), 0o600))
		_, err := LoadConfig(path)
		require.Error(t, err, input)
	}
}

func TestLoadConfigRejectsDuplicateKeysAndTrailingJSON(t *testing.T) {
	dir := t.TempDir()
	for index, input := range []string{
		`{"origin":"https://stage.example","origin":"https://evil.example","token":"secret","models":["model-a"],"timeout_seconds":30,"bridge_sha256":"` + testBridgeSHA256 + `"}`,
		`{"origin":"https://stage.example","token":"secret","models":["model-a"],"timeout_seconds":30,"bridge_sha256":"` + testBridgeSHA256 + `"}{}`,
	} {
		path := filepath.Join(dir, "strict-"+string(rune('a'+index))+".json")
		require.NoError(t, os.WriteFile(path, []byte(input), 0o600))
		_, err := LoadConfig(path)
		require.Error(t, err)
	}
}
