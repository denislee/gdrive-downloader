package main

import (
	"context"
	"log"
	"os"

	"gioui.org/app"
	"gioui.org/unit"
)

func main() {
	model := NewModel()
	if cfg, err := LoadConfig(); err == nil {
		model.SetCredentials(cfg.CredentialsPath)
		model.SetOutputDir(cfg.OutputDir)
		model.SetDeleteAfterDownload(cfg.DeleteAfterDownload)
	}
	go func() {
		w := new(app.Window)
		w.Option(
			app.Title("Google Drive Downloader"),
			app.Size(unit.Dp(900), unit.Dp(640)),
			app.MinSize(unit.Dp(640), unit.Dp(480)),
		)
		ui := newUI(w, model)
		model.SetOnChange(func() { w.Invalidate() })
		if err := ui.run(context.Background()); err != nil {
			log.Println("ui error:", err)
		}
		os.Exit(0)
	}()
	app.Main()
}
