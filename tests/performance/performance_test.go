//go:build integration

package performance_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/service/objectstore"
)

type metric struct {
	Name      string  `json:"name"`
	Value     float64 `json:"value"`
	Unit      string  `json:"unit"`
	Threshold float64 `json:"threshold"`
	Pass      bool    `json:"pass"`
}

func TestNightlyPerformanceThresholds(t *testing.T) {
	metrics := []metric{gatewayLatency(t), uploadThroughput(t)}
	writeMetrics(t, metrics)
	for _, result := range metrics {
		if !result.Pass {
			t.Errorf("%s %.2f%s breached threshold %.2f%s", result.Name, result.Value, result.Unit, result.Threshold, result.Unit)
		}
	}
}

func gatewayLatency(t *testing.T) metric {
	t.Helper()
	client := &http.Client{Timeout: 2 * time.Second}
	url := requiredEnv(t, "TEST_GATEWAY_A_URL") + "/healthz"
	durations := make([]float64, 0, 200)
	for range 200 {
		started := time.Now()
		response, err := client.Get(url)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("health probe returned %d", response.StatusCode)
		}
		durations = append(durations, float64(time.Since(started).Microseconds())/1000)
	}
	sort.Float64s(durations)
	p95 := durations[int(float64(len(durations))*0.95)-1]
	return metric{Name: "gateway_http_p95", Value: p95, Unit: "ms", Threshold: 250, Pass: p95 <= 250}
}

func uploadThroughput(t *testing.T) metric {
	t.Helper()
	store, err := objectstore.NewS3(objectstore.S3Config{
		Endpoint: requiredEnv(t, "TEST_S3_ENDPOINT"), Region: requiredEnv(t, "TEST_S3_REGION"),
		Bucket: requiredEnv(t, "TEST_S3_BUCKET"), AccessKeyID: requiredEnv(t, "TEST_S3_ACCESS_KEY_ID"),
		SecretAccessKey: requiredEnv(t, "TEST_S3_SECRET_ACCESS_KEY"), UsePathStyle: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("codex-gateway-performance\n"), 400_000)
	sum := sha256.Sum256(payload)
	key := fmt.Sprintf("nightly/%d.bin", time.Now().UnixNano())
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	started := time.Now()
	_, err = store.Put(context.Background(), key, bytes.NewReader(payload), objectstore.PutOptions{
		ContentType: "application/octet-stream", Size: int64(len(payload)), ChecksumSHA256: hex.EncodeToString(sum[:]),
	})
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Delete(context.Background(), key) })
	seconds := time.Since(started).Seconds()
	throughput := float64(len(payload)) / (1024 * 1024) / seconds
	allocatedMiB := float64(after.TotalAlloc-before.TotalAlloc) / (1024 * 1024)
	if allocatedMiB > 64 {
		t.Errorf("upload allocated %.2fMiB, threshold 64MiB", allocatedMiB)
	}
	return metric{Name: "s3_upload_throughput", Value: throughput, Unit: "MiB/s", Threshold: 5, Pass: throughput >= 5 && allocatedMiB <= 64}
}

func writeMetrics(t *testing.T, metrics []metric) {
	t.Helper()
	directory := requiredEnv(t, "ARTIFACT_DIR")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	payload, err := json.MarshalIndent(map[string]any{"generated_at": time.Now().UTC(), "metrics": metrics}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "performance.json"), append(payload, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

func requiredEnv(t *testing.T, name string) string {
	t.Helper()
	value := os.Getenv(name)
	if value == "" {
		t.Fatalf("%s is required", name)
	}
	return value
}
