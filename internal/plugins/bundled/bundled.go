// Package bundled holds the plugins that ship with lucode, each in its own folder. They go through
// the same validation as an uploaded zip. Only the hub's host imports this.
package bundled

import "embed"

//go:embed *
var FS embed.FS
