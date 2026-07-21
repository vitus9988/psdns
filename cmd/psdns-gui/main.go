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

// version is a display-only fallback, injected at build time via
// -ldflags "-X main.version=...". selfupdate.Version is the single source of
// truth for the running version (update checks compare against it), so
// displayVersion prefers it; this is used only when self-update is disabled
// (e.g. a dev build that still wants a richer git-describe string on screen).
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

// displayVersion returns the version to show in the UI. selfupdate.Version is
// authoritative — it is what update checks compare against — so display follows
// it whenever it carries a real (injected) version, keeping the shown version
// from diverging from the one updates act on. main.version is only a fallback
// for a build that injects a display-only string while leaving self-update off.
func displayVersion() string {
	if selfupdate.Version != "dev" {
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
