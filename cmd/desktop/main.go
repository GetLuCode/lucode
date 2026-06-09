package main

import (
	"embed"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/linux"
)

//go:embed all:frontend/dist
var assets embed.FS

//go:embed lu-icon.png
var appIcon []byte

func main() {
	app := NewApp()

	err := wails.Run(&options.App{
		Title:            "LuCode",
		Width:            1440,
		Height:           900,
		MinWidth:         800,
		MinHeight:        600,
		Frameless:        true,
		BackgroundColour: &options.RGBA{R: 12, G: 12, B: 14, A: 255},
		AssetServer: &assetserver.Options{
			Assets:  assets,
			Handler: app,
		},
		Linux: &linux.Options{
			Icon:        appIcon,
			ProgramName: "LuCode",
		},
		OnStartup:  app.startup,
		OnShutdown: app.shutdown,
		Bind:       []interface{}{app},
	})
	if err != nil {
		println("Error:", err.Error())
	}
}
