// Package ark holds files embedded into the binary.
package ark

import _ "embed"

//go:embed config.example.yaml
var ExampleConfig []byte
