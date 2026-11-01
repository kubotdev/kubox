package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/kubotdev/kubox/internal/env"
)

func main() {
	dir := "schema"
	if len(os.Args) > 1 {
		dir = os.Args[1]
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	name := fmt.Sprintf("kubox-envelope-%s.json", env.SchemaVersion)
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, env.EnvelopeSchema(), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("wrote", p)
}
