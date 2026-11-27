//go:build embed

package assets

import "embed"

// embedded carries the staged runtime and model for a self-contained build.
// build.ps1 populates data/ and compiles with `-tags embed`.
//
//go:embed all:data
var embedded embed.FS
