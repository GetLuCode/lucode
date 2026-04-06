package webui

import "embed"

// Dist holds the compiled frontend (web/dist).
// Run `npm run build` inside web/ before building the Go binary.
//
//go:embed dist
var Dist embed.FS
