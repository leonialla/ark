// Package sshtunneldaemon holds files embedded into the binary.
package sshtunneldaemon

import _ "embed"

//go:embed config.example.yaml
var ExampleConfig []byte
