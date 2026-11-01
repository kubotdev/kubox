package env

import (
	"encoding/json"

	"github.com/invopop/jsonschema"
)

// JSON Schema for the verification envelope, generated from the Go types.
// Regenerate with `go run ./tools/schemagen` after changing them.
func EnvelopeSchema() []byte {
	r := jsonschema.Reflect(&Report{})
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		panic(err)
	}
	return append(b, '\n')
}
