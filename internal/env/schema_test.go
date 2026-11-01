package env

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Committed schema must equal a fresh generation. Fix: `go run ./tools/schemagen`.
func TestSchema_matchesModel(t *testing.T) {
	want := EnvelopeSchema()
	path := filepath.Join("..", "..", "schema", "kubox-envelope-"+SchemaVersion+".json")
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read committed schema: %v (run `go run ./tools/schemagen`)", err)
	}
	got = []byte(strings.ReplaceAll(string(got), "\r\n", "\n"))
	if string(got) != string(want) {
		t.Fatalf("committed schema %s drifts from model types — run `go run ./tools/schemagen`", path)
	}
}
