// Package sdk holds the type definitions a plugin's author works with, so that lucode-plugin new
// can write them into a new plugin. They are documentation for the editor: nothing in lucode reads
// them.
package sdk

import _ "embed"

//go:embed lucode-plugin.d.ts
var PluginTypes string

//go:embed lucode-ui.d.ts
var UITypes string

//go:embed lucode-test.d.ts
var TestTypes string
