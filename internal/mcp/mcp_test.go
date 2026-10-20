package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestConcurrentToolCalls(t *testing.T) {
	slow := Tool{
		Name: "slow", Description: "slow", InputSchema: map[string]any{"type": "object"},
		Handler: func(context.Context, json.RawMessage) (string, error) {
			time.Sleep(300 * time.Millisecond)
			return "slow-done", nil
		},
	}
	fast := Tool{
		Name: "fast", Description: "fast", InputSchema: map[string]any{"type": "object"},
		Handler: func(context.Context, json.RawMessage) (string, error) {
			return "fast-done", nil
		},
	}
	srv := &Server{Name: "t", Version: "0", Tools: []Tool{slow, fast}}

	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"slow","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"fast","arguments":{}}}`,
		"",
	}, "\n")

	var out strings.Builder
	if err := srv.Serve(context.Background(), strings.NewReader(input), &out); err != nil {
		t.Fatalf("Serve: %v", err)
	}

	idxSlow, idxFast := -1, -1
	for i, line := range strings.Split(out.String(), "\n") {
		switch {
		case strings.Contains(line, `"id":2`):
			idxSlow = i
		case strings.Contains(line, `"id":3`):
			idxFast = i
		}
	}
	if idxSlow < 0 || idxFast < 0 {
		t.Fatalf("missing tool responses:\n%s", out.String())
	}
	if idxFast > idxSlow {
		t.Fatalf("fast response should arrive before slow when calls run concurrently (slow@%d fast@%d):\n%s",
			idxSlow, idxFast, out.String())
	}
}
