// Package christmasdraw holds files embedded from the repository root.
package christmasdraw

import _ "embed"

// ExampleConfig is config.example.yaml: the documented sample config, which
// the demo command also runs.
//
//go:embed config.example.yaml
var ExampleConfig []byte
