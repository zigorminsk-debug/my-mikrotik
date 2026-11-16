package main

import (
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

func TestReportViewerIsReadOnlyAndHighContrast(t *testing.T) {
	a := test.NewApp()
	defer a.Quit()
	a.Settings().SetTheme(inspectorTheme{theme.LightTheme()})
	entry := widget.NewMultiLineEntry()
	entry.SetText("Snapshot ABC 123")
	entry.SetMinRowsVisible(3)
	entry.Disable()
	view := reportViewer(entry)
	w := test.NewWindow(view)
	defer w.Close()
	w.Resize(fyne.NewSize(400, 120))
	w.Show()
	if !entry.Disabled() {
		t.Fatal("report must remain read-only")
	}
	test.Type(entry, "changed")
	entry.TypedKey(&fyne.KeyEvent{Name: fyne.KeyBackspace})
	if entry.Text != "Snapshot ABC 123" {
		t.Fatal(entry.Text)
	}
	img := w.Canvas().Capture()
	bounds := img.Bounds()
	dark := 0
	for y := 8; y < bounds.Dy()-8; y++ {
		for x := 8; x < bounds.Dx()-8; x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			if r < 18000 && g < 18000 && b < 18000 {
				dark++
			}
		}
	}
	if dark < 10 {
		t.Fatalf("report text has insufficient dark pixels: %d", dark)
	}
	// Contrast override must not affect genuinely disabled controls.
	normal := widget.NewButton("Unavailable", func() {})
	normal.Disable()
	_, entryGreen, _, _ := entry.Theme().Color(theme.ColorNameDisabled, theme.VariantLight).RGBA()
	_, normalGreen, _, _ := normal.Theme().Color(theme.ColorNameDisabled, theme.VariantLight).RGBA()
	if normalGreen <= entryGreen {
		t.Fatal("override leaked to other controls")
	}
}
func TestCenteredLayoutAndFactoryReset(t *testing.T) {
	a := test.NewApp()
	defer a.Quit()
	label := widget.NewLabel("content")
	layout := centeredLayout{max: 1120}
	layout.Layout([]fyne.CanvasObject{label}, fyne.NewSize(1400, 100))
	if label.Position().X != 140 || label.Size().Width != 1120 {
		t.Fatal(label.Position(), label.Size())
	}
	layout.Layout([]fyne.CanvasObject{label}, fyne.NewSize(700, 100))
	if label.Position().X != 0 || label.Size().Width != 700 {
		t.Fatal(label.Position(), label.Size())
	}
	host, port, user, password := widget.NewEntry(), widget.NewEntry(), widget.NewEntry(), widget.NewPasswordEntry()
	protocol := widget.NewSelect([]string{"auto", "api", "rest", "mac"}, nil)
	tls := widget.NewCheck("TLS", nil)
	host.SetText("other")
	port.SetText("443")
	user.SetText("other")
	password.SetText("secret")
	protocol.SetSelected("rest")
	tls.SetChecked(true)
	setFactoryConnection(host, port, user, password, protocol, tls)
	if host.Text != "192.168.88.1" || port.Text != "" || user.Text != "admin" || password.Text != "" || protocol.Selected != "auto" || tls.Checked {
		t.Fatal("factory settings were not reset")
	}
}
