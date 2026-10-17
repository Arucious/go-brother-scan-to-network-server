// Package brotherscan holds files embedded from the repository root.
package brotherscan

import _ "embed"

// ExampleConfig is config.yaml.example, written on first start.
//
//go:embed config.yaml.example
var ExampleConfig []byte
