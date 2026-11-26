package main

import (
	"embed"
	"fmt"
	"io/fs"
	"log"
	"time"

	"agyp-desktop/internal/agyp"
	"agyp-desktop/internal/service"

	"github.com/egoist/mygo"
)

//go:embed dist/*
var distFS embed.FS

//go:embed dist/icon.png
var appIconBytes []byte

func main() {
	app := mygo.App
	app.SetName("agyp Monitor")

	frontendFS, err := fs.Sub(distFS, "dist")
	if err != nil {
		log.Fatalf("failed to load frontend: %v", err)
	}
	mygo.SetFrontend(frontendFS)

	var win *mygo.Window
	var flyoutWin *mygo.Window
	var tray *mygo.Tray

	openWindow := func() {
		if flyoutWin != nil {
			flyoutWin.Close()
			flyoutWin = nil
		}
		if win != nil {
			win.Show()
			win.Focus()
			return
		}
		win = mygo.NewWindow(mygo.WindowOptions{
			Title:           "agyp 网关看板",
			Width:           1360,
			Height:          880,
			MinWidth:        1080,
			MinHeight:       650,
			BackgroundColor: "light-dark(#f8fafc, #0e1015)",
			URL:             "/",
			Page: mygo.PageOptions{
				DevTools: mygo.DevToolsEnabled,
			},
		})
		win.OnClosed(func() {
			win = nil
		})
	}

	toggleFlyout := func() {
		if flyoutWin != nil {
			flyoutWin.Close()
			flyoutWin = nil
			return
		}
		if tray == nil {
			openWindow()
			return
		}
		flyoutWin = mygo.NewFlyout(mygo.FlyoutOptions{
			Tray:      tray,
			Placement: mygo.PlacementBottom,
			Width:     310,
			Height:    460,
			Focusable: true,
			Shadow:    true,
			Popover:   true,
			URL:       "/menubar.html",
		})
		flyoutWin.OnClosed(func() {
			flyoutWin = nil
		})
	}

	client := agyp.NewClient("")
	gatewaySvc := service.NewGatewayService(client, openWindow, func() {
		mygo.App.Quit()
	}, func(mode string) {
		switch mode {
		case "dark":
			mygo.Theme.SetSource(mygo.ThemeDark)
		case "light":
			mygo.Theme.SetSource(mygo.ThemeLight)
		default:
			mygo.Theme.SetSource(mygo.ThemeSystem)
		}
	})
	mygo.Bind(gatewaySvc)

	app.WhenReady(func() {
		// Set macOS Dock Icon
		if len(appIconBytes) > 0 && app.Dock != nil {
			if err := app.Dock.SetIcon(appIconBytes); err != nil {
				log.Printf("failed to set dock icon: %v", err)
			}
		}

		// App Menu
		app.SetMenu(mygo.NewMenu([]*mygo.MenuItem{
			{Role: mygo.RoleAppMenu},
			{Label: "File", Submenu: []*mygo.MenuItem{
				{Label: "打开完整看板", Accelerator: "CmdOrCtrl+O", Click: func(_ *mygo.MenuItem, _ *mygo.Window) {
					openWindow()
				}},
				{Label: "切换菜单栏看板", Accelerator: "CmdOrCtrl+M", Click: func(_ *mygo.MenuItem, _ *mygo.Window) {
					toggleFlyout()
				}},
				{Label: "刷新", Accelerator: "CmdOrCtrl+R", Click: func(_ *mygo.MenuItem, w *mygo.Window) {
					if w != nil && w.Page() != nil {
						w.Page().Reload()
					}
				}},
				mygo.Separator(),
				{Role: mygo.RoleClose},
			}},
			{Role: mygo.RoleEditMenu},
			{Role: mygo.RoleViewMenu},
			{Role: mygo.RoleWindowMenu},
		}))

		// MenuBar Tray
		trayMenu := mygo.NewMenu([]*mygo.MenuItem{
			{Label: "展开快捷看板", Click: func(*mygo.MenuItem, *mygo.Window) {
				toggleFlyout()
			}},
			{Label: "打开完整大盘", Accelerator: "CmdOrCtrl+O", Click: func(*mygo.MenuItem, *mygo.Window) {
				openWindow()
			}},
			mygo.Separator(),
			{Role: mygo.RoleQuit},
		})

		var err error
		tray, err = mygo.NewTray(mygo.TrayOptions{
			Title:   "0.0 t/s",
			ToolTip: "agyp 多账号容灾网关监控 (点击展开快捷菜单看板)",
		})
		if err == nil {
			if sizer, ok := any(tray).(interface{ SetTitleFontSize(float64) }); ok {
				sizer.SetTitleFontSize(11.0)
			}
			tray.OnClick(func() {
				toggleFlyout()
			})
			tray.OnRightClick(func() {
				if flyoutWin != nil {
					flyoutWin.Close()
					flyoutWin = nil
				}
				tray.SetMenu(trayMenu)
				defer tray.SetMenu(nil)
				tray.PopUpMenu()
			})

			// Background ticker to update tray title with live speed only
			go func() {
				ticker := time.NewTicker(3 * time.Second)
				defer ticker.Stop()
				for range ticker.C {
					diag, err := client.FetchDiag()
					if err == nil && diag.Online {
						sp := diag.RecentSpeed
						if sp > 0 {
							tray.SetTitle(fmt.Sprintf("%.1f t/s", sp))
						} else {
							tray.SetTitle("0.0 t/s")
						}
					} else {
						tray.SetTitle("- t/s")
					}
				}
			}()
		}

		openWindow()
	})

	// Keep app alive in Dock & MenuBar when window is closed
	app.OnWindowAllClosed(func() {})
	app.OnActivate(func(hasVisibleWindows bool) {
		if !hasVisibleWindows {
			openWindow()
		}
	})

	if err := app.Run(); err != nil {
		log.Fatalf("app error: %v", err)
	}
}
