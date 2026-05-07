package main

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"strings"
	"sync"
	"time"

	"gioui.org/app"
	"gioui.org/font/gofont"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

type ui struct {
	w     *app.Window
	model *Model
	th    *material.Theme

	credEditor   widget.Editor
	outDirEditor widget.Editor

	signInBtn widget.Clickable
	forgetBtn widget.Clickable
	scanBtn   widget.Clickable
	startBtn  widget.Clickable
	stopBtn   widget.Clickable

	filesList widget.List
	logList   widget.List

	mu       sync.Mutex
	cancelDl context.CancelFunc
	dctx     *driveContext

	rootCtx context.Context
}

type driveContext struct {
	driver *Driver
	state  *State
}

func newUI(w *app.Window, m *Model) *ui {
	th := material.NewTheme()
	th.Shaper = text.NewShaper(text.WithCollection(gofont.Collection()))
	u := &ui{w: w, model: m, th: th}
	u.credEditor.SingleLine = true
	u.credEditor.SetText(m.CredentialsPath)
	u.outDirEditor.SingleLine = true
	u.outDirEditor.SetText(m.OutputDir)
	u.filesList.Axis = layout.Vertical
	u.logList.Axis = layout.Vertical
	return u
}

func (u *ui) run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	u.rootCtx = ctx

	var ops op.Ops
	for {
		switch e := u.w.Event().(type) {
		case app.DestroyEvent:
			return e.Err
		case app.FrameEvent:
			gtx := app.NewContext(&ops, e)
			u.layout(gtx)
			e.Frame(gtx.Ops)
		}
	}
}

// ----- layout -----

var (
	colorBg    = color.NRGBA{R: 0xf6, G: 0xf6, B: 0xf8, A: 0xff}
	colorPanel = color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
)

func (u *ui) layout(gtx layout.Context) layout.Dimensions {
	paint.Fill(gtx.Ops, colorBg)

	// Sync editor text into model so other goroutines see it.
	cred := strings.TrimSpace(u.credEditor.Text())
	out := strings.TrimSpace(u.outDirEditor.Text())

	snap := u.model.Snapshot()
	if cred != snap.CredentialsPath || out != snap.OutputDir {
		u.model.SetCredentials(cred)
		u.model.SetOutputDir(out)
		_ = SaveConfig(Config{CredentialsPath: cred, OutputDir: out})
	}

	// Click handling.
	if u.signInBtn.Clicked(gtx) {
		u.startSignIn()
	}
	if u.forgetBtn.Clicked(gtx) {
		u.handleForget()
	}
	if u.scanBtn.Clicked(gtx) {
		u.startScan()
	}
	if u.startBtn.Clicked(gtx) {
		u.startDownload()
	}
	if u.stopBtn.Clicked(gtx) {
		u.stopDownload()
	}

	return layout.UniformInset(unit.Dp(12)).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical, Spacing: layout.SpaceEnd}.Layout(gtx,
			layout.Rigid(u.headerRow),
			rigidSpacer(8),
			layout.Rigid(u.credentialsRow),
			rigidSpacer(6),
			layout.Rigid(u.outputRow),
			rigidSpacer(6),
			layout.Rigid(u.actionsRow),
			rigidSpacer(8),
			layout.Rigid(u.progressRow),
			rigidSpacer(8),
			layout.Flexed(1, u.filesPanel),
			rigidSpacer(8),
			layout.Flexed(0.4, u.logPanel),
		)
	})
}

func (u *ui) headerRow(gtx layout.Context) layout.Dimensions {
	snap := u.model.Snapshot()
	title := material.H6(u.th, "Google Drive Downloader")
	status := material.Body2(u.th, fmt.Sprintf("status: %s — %s", snap.Phase, snap.Message))
	signed := "not signed in"
	if snap.SignedIn {
		signed = "signed in as " + snap.UserEmail
	}
	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(title.Layout),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Left: unit.Dp(16)}.Layout(gtx, status.Layout)
		}),
		layout.Rigid(material.Body2(u.th, signed).Layout),
	)
}

func (u *ui) credentialsRow(gtx layout.Context) layout.Dimensions {
	return panel(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(material.Body1(u.th, "credentials.json:").Layout),
			rigidSpacerW(8),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				return material.Editor(u.th, &u.credEditor, "/path/to/credentials.json").Layout(gtx)
			}),
			rigidSpacerW(8),
			layout.Rigid(material.Button(u.th, &u.signInBtn, "Sign in").Layout),
			rigidSpacerW(6),
			layout.Rigid(material.Button(u.th, &u.forgetBtn, "Forget").Layout),
		)
	})
}

func (u *ui) outputRow(gtx layout.Context) layout.Dimensions {
	return panel(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(material.Body1(u.th, "output folder:").Layout),
			rigidSpacerW(8),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				return material.Editor(u.th, &u.outDirEditor, "/path/to/destination").Layout(gtx)
			}),
		)
	})
}

func (u *ui) actionsRow(gtx layout.Context) layout.Dimensions {
	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(material.Button(u.th, &u.scanBtn, "Scan Drive").Layout),
		rigidSpacerW(8),
		layout.Rigid(material.Button(u.th, &u.startBtn, "Start download").Layout),
		rigidSpacerW(8),
		layout.Rigid(material.Button(u.th, &u.stopBtn, "Stop").Layout),
	)
}

func (u *ui) progressRow(gtx layout.Context) layout.Dimensions {
	snap := u.model.Snapshot()
	var progress float32
	if snap.Total > 0 {
		progress = float32(snap.Done+snap.Skipped) / float32(snap.Total)
	}
	label := fmt.Sprintf("%d / %d  •  skipped %d  •  failed %d  •  %s",
		snap.Done+snap.Skipped, snap.Total, snap.Skipped, snap.Failed, humanBytes(snap.Bytes))
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return material.ProgressBar(u.th, progress).Layout(gtx)
		}),
		rigidSpacer(4),
		layout.Rigid(material.Body2(u.th, label).Layout),
	)
}

func (u *ui) filesPanel(gtx layout.Context) layout.Dimensions {
	snap := u.model.Snapshot()
	visible := make([]FileItem, 0, len(snap.Files))
	for _, f := range snap.Files {
		if f.Status == StatusDownloading || f.Status == StatusFailed {
			visible = append(visible, f)
		}
	}
	if len(visible) == 0 {
		for i := 0; i < len(snap.Files) && len(visible) < 100; i++ {
			visible = append(visible, snap.Files[i])
		}
	}
	return panel(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(material.Body1(u.th, fmt.Sprintf("Files (%d total, showing %d)", len(snap.Files), len(visible))).Layout),
			rigidSpacer(4),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				return material.List(u.th, &u.filesList).Layout(gtx, len(visible), func(gtx layout.Context, i int) layout.Dimensions {
					f := visible[i]
					line := fmt.Sprintf("[%s] %s", f.Status, f.RelPath)
					if f.Status == StatusDownloading && f.Size > 0 {
						line = fmt.Sprintf("[downloading %3d%%] %s", int(100*f.BytesGot/maxInt64(1, f.Size)), f.RelPath)
					} else if f.Status == StatusFailed && f.Err != "" {
						line = fmt.Sprintf("[FAILED] %s — %s", f.RelPath, f.Err)
					}
					return material.Body2(u.th, line).Layout(gtx)
				})
			}),
		)
	})
}

func (u *ui) logPanel(gtx layout.Context) layout.Dimensions {
	logs := u.model.Logs()
	return panel(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(material.Body1(u.th, "Log").Layout),
			rigidSpacer(4),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				return material.List(u.th, &u.logList).Layout(gtx, len(logs), func(gtx layout.Context, i int) layout.Dimensions {
					return material.Body2(u.th, logs[len(logs)-1-i]).Layout(gtx)
				})
			}),
		)
	})
}

// ----- click handlers -----

func (u *ui) startSignIn() {
	snap := u.model.Snapshot()
	if strings.TrimSpace(snap.CredentialsPath) == "" {
		u.model.Logf("set credentials.json path first")
		return
	}
	u.model.SetPhase(PhaseAuthenticating, "signing in...")
	go func() {
		ctx := u.rootCtx
		client, _, err := Authenticate(ctx, snap.CredentialsPath)
		if err != nil {
			u.model.SetPhase(PhaseError, err.Error())
			u.model.Logf("auth: %s", err)
			return
		}
		email, err := FetchUserEmail(ctx, client)
		if err != nil {
			email = "(unknown)"
			u.model.Logf("fetch email: %s", err)
		}
		u.model.SetSignedIn(true, email)
		u.model.SetPhase(PhaseIdle, "signed in as "+email)
		u.model.Logf("signed in as %s", email)

		driver, err := NewDriver(ctx, client, u.model, nil)
		if err != nil {
			u.model.Logf("driver: %s", err)
			return
		}
		u.mu.Lock()
		u.dctx = &driveContext{driver: driver}
		u.mu.Unlock()
	}()
}

func (u *ui) handleForget() {
	if err := ForgetToken(); err != nil {
		u.model.Logf("forget token: %s", err)
		return
	}
	u.model.SetSignedIn(false, "")
	u.model.Logf("token cleared")
}

func (u *ui) startScan() {
	snap := u.model.Snapshot()
	u.mu.Lock()
	dctx := u.dctx
	u.mu.Unlock()
	if dctx == nil || dctx.driver == nil {
		u.model.Logf("sign in first")
		return
	}
	if strings.TrimSpace(snap.OutputDir) == "" {
		u.model.Logf("set output folder first")
		return
	}
	u.model.SetPhase(PhaseScanning, "listing files...")
	go func() {
		items, err := dctx.driver.Scan(u.rootCtx)
		if err != nil {
			u.model.SetPhase(PhaseError, err.Error())
			u.model.Logf("scan: %s", err)
			return
		}
		u.model.ResetFiles(items)
		u.model.SetPhase(PhaseIdle, fmt.Sprintf("scanned %d files", len(items)))
		u.model.Logf("scan complete: %d files", len(items))
	}()
}

func (u *ui) startDownload() {
	snap := u.model.Snapshot()
	if snap.OutputDir == "" {
		u.model.Logf("set output folder first")
		return
	}
	u.mu.Lock()
	dctx := u.dctx
	u.mu.Unlock()
	if dctx == nil || dctx.driver == nil {
		u.model.Logf("sign in first")
		return
	}
	if len(snap.Files) == 0 {
		u.model.Logf("scan Drive first")
		return
	}
	state, err := LoadState(snap.OutputDir)
	if err != nil {
		u.model.Logf("state: %s", err)
		return
	}
	dctx.state = state
	dctx.driver.state = state

	dlCtx, cancel := context.WithCancel(u.rootCtx)
	u.mu.Lock()
	if u.cancelDl != nil {
		u.cancelDl()
	}
	u.cancelDl = cancel
	u.mu.Unlock()

	stop := make(chan struct{})
	go state.AutoFlush(stop, 2*time.Second)

	u.model.SetPhase(PhaseDownloading, "downloading...")
	go func() {
		dctx.driver.Run(dlCtx, snap.OutputDir)
		close(stop)
		_ = state.Flush()
		final := u.model.Snapshot()
		if dlCtx.Err() != nil {
			u.model.SetPhase(PhaseIdle, fmt.Sprintf("stopped (%d/%d done)", final.Done, final.Total))
		} else {
			u.model.SetPhase(PhaseDone, fmt.Sprintf("done: %d ok, %d skipped, %d failed", final.Done, final.Skipped, final.Failed))
		}
	}()
}

func (u *ui) stopDownload() {
	u.mu.Lock()
	if u.cancelDl != nil {
		u.cancelDl()
	}
	u.mu.Unlock()
}

// ----- helpers -----

func panel(gtx layout.Context, child layout.Widget) layout.Dimensions {
	macro := op.Record(gtx.Ops)
	dims := layout.UniformInset(unit.Dp(8)).Layout(gtx, child)
	call := macro.Stop()

	rect := image.Rectangle{Max: dims.Size}
	rrect := clip.UniformRRect(rect, 6)
	paint.FillShape(gtx.Ops, colorPanel, rrect.Op(gtx.Ops))
	call.Add(gtx.Ops)
	return dims
}

func rigidSpacer(dp int) layout.FlexChild {
	return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
		return layout.Spacer{Height: unit.Dp(float32(dp))}.Layout(gtx)
	})
}

func rigidSpacerW(dp int) layout.FlexChild {
	return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
		return layout.Spacer{Width: unit.Dp(float32(dp))}.Layout(gtx)
	})
}

func humanBytes(n int64) string {
	const k = 1024
	if n < k {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KB", "MB", "GB", "TB"}
	v := float64(n) / k
	u := 0
	for v >= k && u < len(units)-1 {
		v /= k
		u++
	}
	return fmt.Sprintf("%.2f %s", v, units[u])
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
