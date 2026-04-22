// Package ui bundles the embedded dashboard assets (HTML templates etc.) into
// the server binary via go:embed. Kept as a top-level package because go:embed
// directives cannot reference paths outside the source file's directory.
package ui

import "embed"

//go:embed templates/*.html
var FS embed.FS
