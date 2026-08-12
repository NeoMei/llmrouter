package onboarding

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	maxChecksumsBytes = 256 * 1024
	checksumTimeout   = 10 * time.Second
)

func ResolveBridgeChecksum(ctx context.Context, origin *url.URL, platform Platform, client *http.Client) (string, error) {
	if origin == nil {
		return "", fmt.Errorf("%w: checksum origin is unavailable", ErrUsage)
	}
	normalized, err := NormalizeServerURL(origin.String())
	if err != nil {
		return "", err
	}
	target := bridgeArtifactName(platform)
	if target == "" {
		return "", fmt.Errorf("%w: bridge artifact platform is unsupported", ErrUnavailable)
	}
	if client == nil {
		client = http.DefaultClient
	}
	contained := *client
	contained.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	requestContext, cancel := context.WithTimeout(ctx, checksumTimeout)
	defer cancel()
	endpoint := *normalized
	endpoint.Path = "/install/checksums.txt"
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return "", fmt.Errorf("%w: checksum request is invalid", ErrNetwork)
	}
	request.Header.Set("Accept", "text/plain")
	request.Header.Set("User-Agent", "llmrouter-onboard/"+OnboardingVersion)
	response, err := contained.Do(request)
	if err != nil {
		return "", fmt.Errorf("%w: checksum request failed", ErrNetwork)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxChecksumsBytes))
		return "", fmt.Errorf("%w: checksum status %d", ErrNetwork, response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxChecksumsBytes+1))
	if err != nil || len(body) > maxChecksumsBytes {
		return "", fmt.Errorf("%w: checksum file is unreadable or too large", ErrNetwork)
	}
	return checksumForArtifact(string(body), target)
}

func bridgeArtifactName(platform Platform) string {
	if platform.OS != "linux" && platform.OS != "darwin" && platform.OS != "windows" {
		return ""
	}
	if platform.Arch != "amd64" && platform.Arch != "arm64" {
		return ""
	}
	name := "llmrouter-mcp-bridge-" + platform.OS + "-" + platform.Arch
	if platform.OS == "windows" {
		name += ".exe"
	}
	return name
}

func checksumForArtifact(body, target string) (string, error) {
	lines := strings.Split(body, "\n")
	seenLines := make(map[string]struct{}, len(lines))
	seenNames := make(map[string]struct{}, len(lines))
	result := ""
	for index, line := range lines {
		if line == "" && index == len(lines)-1 {
			continue
		}
		if line == "" || len(line) < 67 || line[64:66] != "  " {
			return "", fmt.Errorf("%w: checksum file has invalid grammar", ErrNetwork)
		}
		if _, duplicate := seenLines[line]; duplicate {
			return "", fmt.Errorf("%w: checksum file has a duplicate line", ErrNetwork)
		}
		seenLines[line] = struct{}{}
		digest, name := line[:64], line[66:]
		if !validSHA256(digest) || !validChecksumFilename(name) {
			return "", fmt.Errorf("%w: checksum file has invalid grammar", ErrNetwork)
		}
		if _, duplicate := seenNames[name]; duplicate {
			return "", fmt.Errorf("%w: checksum file has a duplicate artifact", ErrNetwork)
		}
		seenNames[name] = struct{}{}
		if name == target {
			result = strings.ToLower(digest)
		}
	}
	if result == "" {
		return "", fmt.Errorf("%w: checksum file does not contain the selected bridge", ErrNetwork)
	}
	return result, nil
}

func validChecksumFilename(name string) bool {
	if name == "" || strings.Contains(name, "..") {
		return false
	}
	for _, character := range name {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || strings.ContainsRune("._-", character) {
			continue
		}
		return false
	}
	return true
}
