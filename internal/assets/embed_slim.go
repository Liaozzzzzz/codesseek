//go:build !embed

package assets

import "embed"

// embedded stays empty for the default (slim) build. fs.Stat on the zero
// embed.FS always fails, so HasRuntime and HasModel report false and callers
// use the runtime and model found on disk.
var embedded embed.FS
