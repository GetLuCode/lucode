//go:build darwin

package main

import _ "embed"

// embeddedHook is the lucodehook binary for darwin, baked into the runner at build time
// (see build-runner.sh and build.sh). It is the approval hook claude calls before a tool
// runs; the hub answers it. One file per OS, so each build embeds only its own.
//
//go:embed embedded/lucodehook-darwin
var embeddedHook []byte
