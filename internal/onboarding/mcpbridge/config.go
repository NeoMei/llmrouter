package mcpbridge

import (
	json "encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strings"

	"github.com/QuantumNous/new-api/common"
)

const maxConfigBytes = 64 * 1024

type Config struct {
	Origin         string   `json:"origin"`
	Token          string   `json:"token"`
	Models         []string `json:"models"`
	TimeoutSeconds int      `json:"timeout_seconds"`
}

func LoadConfig(path string) (Config, error) {
	if path == "" || hasControl(path) {
		return Config{}, fmt.Errorf("credential config path is invalid")
	}
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Mode().Perm()&0o077 != 0 {
		return Config{}, fmt.Errorf("credential config must be a private regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("open credential config")
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) || !after.Mode().IsRegular() || after.Mode().Perm()&0o077 != 0 {
		return Config{}, fmt.Errorf("credential config changed while opening")
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxConfigBytes+1))
	if err != nil || len(raw) > maxConfigBytes {
		return Config{}, fmt.Errorf("credential config is unreadable or too large")
	}
	var fields map[string]json.RawMessage
	if err := common.UnmarshalUseNumber(raw, &fields); err != nil || fields == nil {
		return Config{}, fmt.Errorf("credential config is invalid JSON")
	}
	for name := range fields {
		if name != "origin" && name != "token" && name != "models" && name != "timeout_seconds" {
			return Config{}, fmt.Errorf("credential config contains an unknown field")
		}
	}
	if len(fields) != 4 {
		return Config{}, fmt.Errorf("credential config is incomplete")
	}
	var config Config
	if err := common.Unmarshal(raw, &config); err != nil {
		return Config{}, fmt.Errorf("credential config has invalid field types")
	}
	if err := config.Validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

func (config Config) Validate() error {
	origin, err := url.Parse(config.Origin)
	if err != nil || origin.Scheme == "" || origin.Host == "" || origin.User != nil || origin.RawQuery != "" || origin.Fragment != "" {
		return fmt.Errorf("credential origin is invalid")
	}
	if origin.Path != "" && origin.Path != "/" {
		return fmt.Errorf("credential origin must not include a path")
	}
	if origin.Scheme != "https" && !(origin.Scheme == "http" && isLoopbackHost(origin.Hostname())) {
		return fmt.Errorf("credential origin must use HTTPS except on loopback")
	}
	if config.Token == "" || hasControl(config.Token) {
		return fmt.Errorf("credential token is invalid")
	}
	if len(config.Models) == 0 || len(config.Models) > 256 {
		return fmt.Errorf("credential model allow-list is invalid")
	}
	seen := make(map[string]struct{}, len(config.Models))
	for _, model := range config.Models {
		if model == "" || len(model) > 512 || hasControl(model) {
			return fmt.Errorf("credential model allow-list contains an invalid model")
		}
		if _, exists := seen[model]; exists {
			return fmt.Errorf("credential model allow-list contains a duplicate model")
		}
		seen[model] = struct{}{}
	}
	if config.TimeoutSeconds < 1 || config.TimeoutSeconds > 30 {
		return fmt.Errorf("credential timeout must be between 1 and 30 seconds")
	}
	return nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func hasControl(value string) bool {
	return strings.IndexFunc(value, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0
}
