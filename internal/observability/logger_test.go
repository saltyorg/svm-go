package observability

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestLoggerWritesStructuredJSON(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	logger := NewLogger(&output)

	logger.Info("startup", String("port", "8080"), Int("workers", 2))

	line := strings.TrimSpace(output.String())
	if line == "" {
		t.Fatal("expected log output, got empty string")
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(line), &payload); err != nil {
		t.Fatalf("expected valid json log line, got error: %v", err)
	}

	if got := payload["level"]; got != "info" {
		t.Fatalf("expected level %q, got %v", "info", got)
	}
	if got := payload["msg"]; got != "startup" {
		t.Fatalf("expected message %q, got %v", "startup", got)
	}
	if got := payload["port"]; got != "8080" {
		t.Fatalf("expected port %q, got %v", "8080", got)
	}

	workers, ok := payload["workers"].(float64)
	if !ok {
		t.Fatalf("expected workers to decode as number, got %T", payload["workers"])
	}
	if workers != 2 {
		t.Fatalf("expected workers 2, got %v", workers)
	}

	ts, ok := payload["ts"].(string)
	if !ok || ts == "" {
		t.Fatalf("expected non-empty timestamp string, got %v", payload["ts"])
	}
	if _, err := time.Parse(time.RFC3339Nano, ts); err != nil {
		t.Fatalf("expected RFC3339Nano timestamp, got %q: %v", ts, err)
	}
}
