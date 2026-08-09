//go:build integration

package fault_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

type faultResult struct {
	Dependency        string  `json:"dependency"`
	RecoveryMS        float64 `json:"recovery_ms"`
	LivenessOK        bool    `json:"liveness_ok"`
	ReadinessDegraded bool    `json:"readiness_degraded"`
}

func TestDependencyLossPreservesLivenessAndRecoversReadiness(t *testing.T) {
	composeFile := requiredEnv(t, "INTEGRATION_COMPOSE_FILE")
	gateway := requiredEnv(t, "TEST_GATEWAY_A_URL")
	results := make([]faultResult, 0, 3)
	for _, dependency := range []string{"postgres", "redis", "minio"} {
		t.Run(dependency, func(t *testing.T) {
			compose(t, composeFile, "stop", dependency)
			degraded := waitForStatus(gateway+"/readyz", func(code int) bool { return code >= 500 }, 30*time.Second)
			live := waitForStatus(gateway+"/healthz", func(code int) bool { return code == http.StatusOK }, 10*time.Second)
			started := time.Now()
			compose(t, composeFile, "up", "--detach", "--wait", dependency)
			recovered := waitForStatus(gateway+"/readyz", func(code int) bool { return code == http.StatusOK }, 45*time.Second)
			if !degraded || !live || !recovered {
				t.Fatalf("dependency=%s degraded=%t live=%t recovered=%t", dependency, degraded, live, recovered)
			}
			results = append(results, faultResult{dependency, float64(time.Since(started).Milliseconds()), live, degraded})
		})
	}
	writeArtifact(t, "fault-recovery.json", results)
}

func TestExternalUpstreamFailureIsIsolated(t *testing.T) {
	composeFile := requiredEnv(t, "INTEGRATION_COMPOSE_FILE")
	gateway := requiredEnv(t, "TEST_GATEWAY_A_URL")
	upstream := requiredEnv(t, "TEST_UPSTREAM_URL")
	compose(t, composeFile, "stop", "upstream")
	defer compose(t, composeFile, "up", "--detach", "--wait", "upstream")
	if !waitForStatus(upstream, func(code int) bool { return code == 0 }, 10*time.Second) {
		t.Fatal("upstream did not become unavailable")
	}
	for _, endpoint := range []string{"/healthz", "/readyz"} {
		if !waitForStatus(gateway+endpoint, func(code int) bool { return code == http.StatusOK }, 10*time.Second) {
			t.Fatalf("gateway endpoint %s failed with an isolated upstream outage", endpoint)
		}
	}
}

func TestSecondGatewaySurvivesPeerLoss(t *testing.T) {
	composeFile := requiredEnv(t, "INTEGRATION_COMPOSE_FILE")
	peer := requiredEnv(t, "TEST_GATEWAY_B_URL")
	compose(t, composeFile, "stop", "gateway-a")
	defer compose(t, composeFile, "up", "--detach", "--wait", "gateway-a")
	if !waitForStatus(peer+"/readyz", func(code int) bool { return code == http.StatusOK }, 15*time.Second) {
		t.Fatal("gateway-b was not ready after gateway-a stopped")
	}
}

func compose(t *testing.T, file string, args ...string) {
	t.Helper()
	commandArgs := append([]string{"compose", "-f", file}, args...)
	command := exec.Command("docker", commandArgs...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("docker %v: %v\n%s", commandArgs, err, output)
	}
}

func waitForStatus(url string, accept func(int) bool, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: time.Second}
	for time.Now().Before(deadline) {
		response, err := client.Get(url)
		code := 0
		if err == nil {
			code = response.StatusCode
			_ = response.Body.Close()
		}
		if accept(code) {
			return true
		}
		time.Sleep(250 * time.Millisecond)
	}
	return false
}

func requiredEnv(t *testing.T, name string) string {
	t.Helper()
	value := os.Getenv(name)
	if value == "" {
		t.Fatalf("%s is required", name)
	}
	return value
}

func writeArtifact(t *testing.T, name string, value any) {
	t.Helper()
	directory := requiredEnv(t, "ARTIFACT_DIR")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	payload, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, name), append(payload, '\n'), 0o644); err != nil {
		t.Fatal(fmt.Errorf("write artifact: %w", err))
	}
}
