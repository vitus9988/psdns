// Command psdns-gui is the desktop GUI for psdns. It wraps the same DNS/SNI
// bypass engine the CLI uses (internal/supervisor over internal/{doh,resolver,
// dnssrv,proxy}) in a Wails native window with a Toss-styled control panel, and
// keeps itself up to date via internal/selfupdate.
package main

import (
	"embed"
	"log"
	"os"

	"github.com/vitus9988/psdns/internal/gui"
	"github.com/vitus9988/psdns/internal/relaunch"
	"github.com/vitus9988/psdns/internal/selfupdate"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
)

// version is the release version, injected at build time via
// -ldflags "-X main.version=...". The self-update logic reads its own copy from
// internal/selfupdate.Version (also injected); this one is for display.
var version = "dev"

//go:embed all:frontend/dist
var assets embed.FS

// logFatal is a test seam; in production it is exactly log.Fatal.
var logFatal = log.Fatal

func main() {
	if err := run(os.Args[1:]); err != nil {
		logFatal(err)
	}
}

// run is main's body behind a testable boundary: the relaunch-helper handoff
// and version plumbing can run under a test, while the Wails launch itself
// cannot.
func run(args []string) error {
	if handled, err := relaunch.Run(args); handled {
		return err
	}
	return wails.Run(appOptions(gui.NewApp(displayVersion())))
}

// displayVersion keeps the display version and the self-update version in sync
// when only one was injected at build time.
func displayVersion() string {
	if version == "dev" && selfupdate.Version != "dev" {
		return selfupdate.Version
	}
	return version
}

func appOptions(app *gui.App) *options.App {
	return &options.App{
		Title:            "psdns",
		Width:            480,
		Height:           860,
		MinWidth:         380,
		MinHeight:        600,
		BackgroundColour: &options.RGBA{R: 249, G: 250, B: 251, A: 255},
		AssetServer:      &assetserver.Options{Assets: assets},
		OnStartup:        app.Startup,
		OnShutdown:       app.Shutdown,
		OnBeforeClose:    app.BeforeClose,
		Bind:             []interface{}{app},
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId:               "psdns-gui.vitus9988.github.io",
			OnSecondInstanceLaunch: app.OnSecondInstance,
		},
		Mac: &mac.Options{
			TitleBar:             mac.TitleBarHiddenInset(),
			Appearance:           mac.DefaultAppearance,
			WebviewIsTransparent: false,
		},
	}
}
