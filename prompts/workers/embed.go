// Package workers provides the versioned worker prompt sources.
package workers

import _ "embed"

var (
	//go:embed common.md
	Common string

	//go:embed implementation.md
	Implementation string
)
