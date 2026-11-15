package main

import (
	"context"
	"fmt"
	"github.com/zigorminsk-debug/my-mikrotik/fyneapp/internal/viewmodel"
	"strings"
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
	g := newGeoPanel(func() (mikrotik.Params, *mikrotik.Info, bool) { return mikrotik.Params{}, device, connected }, func(time.Duration) (context.Context, func() bool) { t.Fatal("unexpected operation"); return nil, nil }, func(string, string, func(bool)) { t.Fatal("unexpected confirm") })
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
	g.ack.SetChecked(true)
	g.output.SetText("old preview")
	before := g.revision
	g.countries.SetText("de")
	if g.revision <= before || len(g.loadedPrefixes) != 0 || g.output.Text != "" || g.ack.Checked {
		t.Fatal("country change left stale result")
	}
	g.setBusy(true)
	if !g.countries.Disabled() || !g.check.Disabled() || !g.refresh.Disabled() {
		t.Fatal("controls enabled during operation")
	}
	g.setBusy(false)
	g.interfaces = []mikrotik.Iface{{Name: "ether1"}}
	g.loadedCodes = []string{"by"}
	g.ack.SetChecked(true)
	g.reset()
	if len(g.interfaces) != 0 || len(g.loadedCodes) != 0 || g.output.Text != "" || g.ack.Checked {
		t.Fatal("device change left old data")
	}
}

func geoApplyFixture(t *testing.T) (*geoPanel, *mikrotik.Info, *func(bool), chan mikrotik.GeoApplyRequest, chan struct{}) {
	t.Helper()
	device := &mikrotik.Info{Protocol: "api", Identity: "router", RouterOS: "7.25.1", Architecture: "arm", Packages: []mikrotik.Package{{Name: "routeros"}}}
	conn := mikrotik.Params{Protocol: "api", Host: "192.0.2.1", User: "admin"}
	var callback func(bool)
	requests := make(chan mikrotik.GeoApplyRequest, 2)
	done := make(chan struct{}, 2)
	var g *geoPanel
	get := func() (mikrotik.Params, *mikrotik.Info, bool) { return conn, device, true }
	start := func(d time.Duration) (context.Context, func() bool) {
		ctx, cancel := context.WithTimeout(context.Background(), d)
		g.setBusy(true)
		return ctx, func() bool { cancel(); g.setBusy(false); return true }
	}
	g = newGeoPanel(get, start, func(_, _ string, cb func(bool)) { callback = cb })
	g.dispatch = func(f func()) { f(); done <- struct{}{} }
	g.applyFn = func(_ context.Context, r mikrotik.GeoApplyRequest) (string, error) { requests <- r; return "api", nil }
	g.resource.SetText("by")
	g.destination.SetText("192.168.88.50")
	g.port.SetText("3389")
	g.wan.Options = []string{"ether1"}
	g.wan.SetSelected("ether1")
	g.manual.SetText("192.0.2.1")
	g.ack.SetChecked(true)
	geo := g.params()
	p, err := viewmodel.BuildGeoPlan(device, []mikrotik.Iface{{Name: "ether1"}}, geo, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	g.plan = p
	g.planDevice = device
	g.setBusy(false)
	return g, device, &callback, requests, done
}
func TestGeoApplyConfirmAndInvalidate(t *testing.T) {
	a := test.NewApp()
	defer a.Quit()
	g, _, cb, requests, _ := geoApplyFixture(t)
	if g.apply.Disabled() {
		t.Fatal("current preview not enabled")
	}
	g.apply.OnTapped()
	if !g.confirming || *cb == nil {
		t.Fatal("no explicit confirmation")
	}
	(*cb)(false)
	select {
	case <-requests:
		t.Fatal("cancel wrote")
	default:
	}
	if g.confirming || g.writing {
		t.Fatal("cancel still busy")
	}
	g.apply.OnTapped()
	g.destination.SetText("192.168.88.51")
	(*cb)(true)
	select {
	case <-requests:
		t.Fatal("stale confirmation wrote")
	default:
	}
	if !strings.Contains(g.status.Text, "изменились") || g.plan != nil {
		t.Fatal(g.status.Text)
	}
	g.unknown.SetChecked(true)
	g.resource.SetText("other")
	if g.unknown.Checked {
		t.Fatal("unknown NAT override survived target change")
	}
}
func TestGeoApplySendsExactPreviewOnceAndResets(t *testing.T) {
	a := test.NewApp()
	defer a.Quit()
	g, _, cb, requests, done := geoApplyFixture(t)
	expected := g.plan.Request.Script
	g.apply.OnTapped()
	(*cb)(true)
	(*cb)(true)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("no result")
	}
	r := <-requests
	if r.Script != expected || r.Conn.Protocol != "api" || r.AllowUnknownNATStatus {
		t.Fatal("wrong apply request")
	}
	select {
	case <-requests:
		t.Fatal("duplicate write")
	default:
	}
	if g.writing || g.ack.Checked || g.plan != nil || !g.apply.Disabled() || !strings.Contains(g.status.Text, "не подтверждает защиту") {
		t.Fatal("result/reset failed", g.status.Text)
	}
}
func TestGeoApplyUnknownAndFailure(t *testing.T) {
	a := test.NewApp()
	defer a.Quit()
	g, device, cb, requests, done := geoApplyFixture(t)
	g.unknown.SetChecked(true)
	g.ack.SetChecked(true)
	plan, err := viewmodel.BuildGeoPlan(device, []mikrotik.Iface{{Name: "ether1"}}, g.params(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	g.plan = plan
	g.planDevice = device
	g.setBusy(false)
	g.applyFn = func(_ context.Context, r mikrotik.GeoApplyRequest) (string, error) {
		requests <- r
		return "", fmt.Errorf("lost response")
	}
	g.apply.OnTapped()
	(*cb)(true)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("no result")
	}
	r := <-requests
	if !r.AllowUnknownNATStatus || g.unknown.Checked || !strings.Contains(g.status.Text, "частично") || !strings.Contains(g.status.Text, "не повторяйте") {
		t.Fatal(g.status.Text)
	}
}
func TestGeoApplyExpiredAndOversize(t *testing.T) {
	a := test.NewApp()
	defer a.Quit()
	g, _, _, _, _ := geoApplyFixture(t)
	g.plan.Request.GeneratedAt = time.Now().Add(-11 * time.Minute)
	g.setBusy(false)
	if !g.apply.Disabled() {
		t.Fatal("expired preview")
	}
	g.plan.Request.GeneratedAt = time.Now()
	g.plan.Request.Script = strings.Repeat("x", mikrotik.MaxApplyScriptSize+1)
	g.setBusy(false)
	if !g.apply.Disabled() {
		t.Fatal("oversized preview")
	}
}

func TestGeoApplyConnectionChangedWhileConfirming(t *testing.T) {
	a := test.NewApp()
	defer a.Quit()
	g, device, cb, requests, _ := geoApplyFixture(t)
	g.apply.OnTapped()
	g.get = func() (mikrotik.Params, *mikrotik.Info, bool) {
		return mikrotik.Params{Host: "192.0.2.2", Protocol: "api", User: "admin"}, device, true
	}
	(*cb)(true)
	select {
	case <-requests:
		t.Fatal("wrote to changed connection")
	default:
	}
	if !strings.Contains(g.status.Text, "изменились") {
		t.Fatal(g.status.Text)
	}
}
func TestGeoApplyPreflightFailureResetsPreview(t *testing.T) {
	a := test.NewApp()
	defer a.Quit()
	g, _, cb, requests, done := geoApplyFixture(t)
	g.applyFn = func(_ context.Context, r mikrotik.GeoApplyRequest) (string, error) {
		requests <- r
		return "", &mikrotik.GeoPreflightError{Cause: fmt.Errorf("not found")}
	}
	g.apply.OnTapped()
	(*cb)(true)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("no result")
	}
	<-requests
	if !strings.Contains(g.status.Text, "до записи") || strings.Contains(g.status.Text, "частично") || g.plan != nil || g.ack.Checked {
		t.Fatal(g.status.Text)
	}
}
