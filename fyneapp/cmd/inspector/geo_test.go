package main

import (
	"context"
	"testing"
	"time"

	"fyne.io/fyne/v2/test"
	"github.com/zigorminsk-debug/my-mikrotik/mikrotik"
)

func TestGeoPanelInvalidatesAndGates(t *testing.T) {
	a := test.NewApp()
	defer a.Quit()
	var connected bool
	device := &mikrotik.Info{Protocol: "api"}
	g := newGeoPanel(func() (mikrotik.Params, *mikrotik.Info, bool) { return mikrotik.Params{}, device, connected }, func(time.Duration) (context.Context, func() bool) { t.Fatal("unexpected operation"); return nil, nil })
	if !g.check.Disabled() || !g.preview.Disabled() {
		t.Fatal("disconnected actions enabled")
	}
	connected = true
	g.setBusy(false)
	if g.check.Disabled() || g.preview.Disabled() {
		t.Fatal("connected actions disabled")
	}
	g.loadedCodes = []string{"by"}
	g.loadedPrefixes = []string{"192.0.2.0/24"}
	g.output.SetText("old preview")
	before := g.revision
	g.countries.SetText("de")
	if g.revision <= before || len(g.loadedPrefixes) != 0 || g.output.Text != "" {
		t.Fatal("country change left stale result")
	}
	g.setBusy(true)
	if !g.countries.Disabled() || !g.check.Disabled() || !g.refresh.Disabled() {
		t.Fatal("controls enabled during operation")
	}
	g.setBusy(false)
	g.interfaces = []mikrotik.Iface{{Name: "ether1"}}
	g.loadedCodes = []string{"by"}
	g.reset()
	if len(g.interfaces) != 0 || len(g.loadedCodes) != 0 || g.output.Text != "" {
		t.Fatal("device change left old data")
	}
}
