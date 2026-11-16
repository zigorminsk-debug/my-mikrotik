package main

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// A light, high-contrast palette close to the established desktop interface.
// Disabled controls still use Fyne's disabled colours; only report viewers
// override them so non-editable text is not rendered almost white.
type inspectorTheme struct{ fyne.Theme }

func (t inspectorTheme) Color(n fyne.ThemeColorName, v fyne.ThemeVariant) color.Color {
	switch n {
	case theme.ColorNameBackground:
		return color.NRGBA{R: 235, G: 241, B: 249, A: 255}
	case theme.ColorNameForeground:
		return color.NRGBA{R: 30, G: 41, B: 59, A: 255}
	case theme.ColorNamePrimary:
		return color.NRGBA{R: 37, G: 99, B: 235, A: 255}
	}
	return t.Theme.Color(n, theme.VariantLight)
}

type reportTheme struct{ fyne.Theme }

func (t reportTheme) Color(n fyne.ThemeColorName, v fyne.ThemeVariant) color.Color {
	if n == theme.ColorNameDisabled {
		return color.NRGBA{R: 30, G: 41, B: 59, A: 255}
	}
	if n == theme.ColorNameInputBackground {
		return color.NRGBA{R: 249, G: 251, B: 254, A: 255}
	}
	return t.Theme.Color(n, v)
}

// The widget remains disabled to prevent editing a report/plan, including
// keyboard and paste actions. The override applies ONLY to the viewer subtree.
func reportViewer(entry *widget.Entry) fyne.CanvasObject {
	return container.NewThemeOverride(entry, reportTheme{fyne.CurrentApp().Settings().Theme()})
}
func section(title string, objects ...fyne.CanvasObject) fyne.CanvasObject {
	heading := widget.NewLabelWithStyle(title, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	body := append([]fyne.CanvasObject{heading}, objects...)
	bg := canvas.NewRectangle(color.White)
	bg.CornerRadius = 10
	bg.StrokeColor = color.NRGBA{R: 217, G: 225, B: 235, A: 255}
	bg.StrokeWidth = 1
	return container.NewStack(bg, container.NewPadded(container.NewVBox(body...)))
}
func field(title string, control fyne.CanvasObject) fyne.CanvasObject {
	return container.NewVBox(widget.NewLabel(title), control)
}
func appHeader(version string) fyne.CanvasObject {
	bg := canvas.NewRectangle(color.NRGBA{R: 23, G: 35, B: 54, A: 255})
	title := canvas.NewText("MikroTik Inspector", color.White)
	title.TextStyle = fyne.TextStyle{Bold: true}
	title.TextSize = 19
	sub := canvas.NewText("модель · прошивка · пакеты · конструктор", color.NRGBA{R: 173, G: 190, B: 210, A: 255})
	sub.TextSize = 12
	badge := canvas.NewText("Версия: "+version, color.NRGBA{R: 205, G: 216, B: 230, A: 255})
	badge.TextSize = 12
	line := container.NewHBox(title, sub, layout.NewSpacer(), badge)
	return container.NewStack(bg, container.NewPadded(line))
}

// Keeps the desktop content centred, while allowing it to shrink in small windows.
type centeredLayout struct{ max float32 }

func (l centeredLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	var h float32
	for _, o := range objects {
		if o.Visible() && o.MinSize().Height > h {
			h = o.MinSize().Height
		}
	}
	return fyne.NewSize(0, h)
}
func (l centeredLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	width := size.Width
	if width > l.max {
		width = l.max
	}
	for _, o := range objects {
		if !o.Visible() {
			continue
		}
		o.Move(fyne.NewPos((size.Width-width)/2, 0))
		o.Resize(fyne.NewSize(width, size.Height))
	}
}
func centeredPage(o fyne.CanvasObject) fyne.CanvasObject {
	return container.New(centeredLayout{max: 1120}, o)
}
