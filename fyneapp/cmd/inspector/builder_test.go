package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"github.com/zigorminsk-debug/my-mikrotik/mikrotik"
)

func builderFixture(t *testing.T) (*builderPanel, *mikrotik.Info, *bool, *string, *func(fyne.URIWriteCloser, error), chan struct{}) {
	t.Helper()
	d := &mikrotik.Info{Identity: "router", Model: "test", RouterOS: "7.25.1", Architecture: "arm", Protocol: "api", Packages: []mikrotik.Package{{Name: "routeros"}}}
	p := mikrotik.Params{Host: "192.0.2.1", Protocol: "api", User: "admin"}
	connected := true
	copied := ""
	var save func(fyne.URIWriteCloser, error)
	done := make(chan struct{}, 2)
	var b *builderPanel
	b = newBuilderPanel(func() (mikrotik.Params, *mikrotik.Info, bool) { return p, d, connected }, func(timeout time.Duration) (context.Context, func() bool) {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		b.setBusy(true)
		return ctx, func() bool { cancel(); b.setBusy(false); return true }
	}, func(s string) { copied = s }, func(cb func(fyne.URIWriteCloser, error)) { save = cb })
	b.dispatch = func(f func()) { f(); done <- struct{}{} }
	b.setInterfaces(p, d, []mikrotik.Iface{{Name: "ether1", Type: "ether"}, {Name: "ether2", Type: "ether", Bridge: "bridge"}, {Name: "bridge", Type: "bridge"}, {Name: "wlan1", Type: "wireless"}})
	return b, d, &connected, &copied, &save, done
}
func TestBuilderPreviewAndValidation(t *testing.T) {
	a := test.NewApp()
	defer a.Quit()
	b, _, connected, copied, _, _ := builderFixture(t)
	if b.lanIface.Selected != "bridge" || b.dhcpIface.Selected != "bridge" || b.pppoeIface.Selected != "ether1" || b.wifiIface.Selected != "wlan1" {
		t.Fatal("interface defaults")
	}
	b.checks["lan"].SetChecked(true)
	b.checks["dhcp"].SetChecked(true)
	if !strings.Contains(b.script, "MTI-LAN-PREVIEW-ONLY") || b.copyButton.Disabled() {
		t.Fatal(b.status.Text, b.script)
	}
	b.lanIface.SetSelected("ether2")
	if b.script != "" || !b.copyButton.Disabled() || !strings.Contains(b.status.Text, "bridge") {
		t.Fatal("bridge member accepted", b.status.Text)
	}
	b.lanIface.SetSelected("bridge")
	b.poolStart.SetText("192.168.88.200")
	if b.script != "" || !strings.Contains(b.status.Text, "DHCP") {
		t.Fatal("incomplete range accepted", b.status.Text)
	}
	b.poolEnd.SetText("192.168.88.210")
	if b.script == "" {
		t.Fatal(b.status.Text)
	}
	b.copyButton.OnTapped()
	if *copied != b.script {
		t.Fatal("copied stale text")
	}
	*connected = false
	b.copyButton.OnTapped()
	if *copied != b.script {
		t.Fatal("unexpected clipboard")
	}
	b.reset()
	if b.script != "" || b.checks["lan"].Checked || b.copyButton.Disabled() == false {
		t.Fatal("stale plan survived reconnect")
	}
}
func TestBuilderDNSWiFiAndSecretReset(t *testing.T) {
	a := test.NewApp()
	defer a.Quit()
	b, _, _, _, _, _ := builderFixture(t)
	b.checks["dns"].SetChecked(true)
	if b.script == "" || !strings.Contains(b.script, "8.8.8.8") {
		t.Fatal(b.status.Text)
	}
	b.dnsServers.SetText("not an ip")
	if b.script != "" || !strings.Contains(b.status.Text, "DNS") {
		t.Fatal("invalid DNS", b.status.Text)
	}
	b.dnsServers.SetText("1.1.1.1")
	b.checks["wifi"].SetChecked(true)
	b.wifiSSID.SetText("testssid")
	b.wifiPass.SetText("12345678")
	if b.script == "" || !strings.Contains(b.script, "testssid") {
		t.Fatal(b.status.Text)
	}
	b.pppoePass.SetText("private")
	b.reset()
	if b.wifiPass.Text != "" || b.pppoePass.Text != "" || b.output.Text != "" {
		t.Fatal("secrets survived connection change")
	}
}
func TestBuilderUnavailableAndStaleExport(t *testing.T) {
	a := test.NewApp()
	defer a.Quit()
	b, device, _, _, save, _ := builderFixture(t)
	b.setInterfaces(b.conn, device, []mikrotik.Iface{{Name: "bridge", Type: "bridge"}})
	if !b.checks["wifi"].Disabled() {
		t.Fatal("Wi-Fi incorrectly available")
	}
	b.checks["dns"].SetChecked(true)
	b.download.OnTapped()
	if *save == nil {
		t.Fatal("no save dialog")
	}
	b.dnsServers.SetText("9.9.9.9")
	w := &writeFile{}
	(*save)(w, nil)
	if w.Len() != 0 || !w.closed || !strings.Contains(b.status.Text, "не сохранён") {
		t.Fatal(b.status.Text, w.String())
	}
}
func TestBuilderInterfaceReadRejectsStaleAndErrors(t *testing.T) {
	a := test.NewApp()
	defer a.Quit()
	b, _, _, _, _, done := builderFixture(t)
	b.read = func(context.Context, mikrotik.Params) ([]mikrotik.Iface, error) {
		return nil, fmt.Errorf("no bridge ports")
	}
	b.loadInterfaces()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("no result")
	}
	if b.loaded || b.script != "" || !strings.Contains(b.status.Text, "не прочитаны") {
		t.Fatal(b.status.Text)
	}
	b.read = func(context.Context, mikrotik.Params) ([]mikrotik.Iface, error) {
		return []mikrotik.Iface{{Name: "new"}}, nil
	}
	b.dispatch = func(f func()) { b.reset(); f(); done <- struct{}{} }
	b.loadInterfaces()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("no result")
	}
	if b.loaded || b.script != "" {
		t.Fatal("stale interfaces restored")
	}
}

func TestBuilderExportFullExactPreview(t *testing.T) {
	a := test.NewApp()
	defer a.Quit()
	b, _, _, _, save, _ := builderFixture(t)
	b.checks["ntp"].SetChecked(true)
	want := b.script
	if want == "" {
		t.Fatal(b.status.Text)
	}
	b.download.OnTapped()
	w := &writeFile{}
	(*save)(w, nil)
	if !w.closed || w.String() != want {
		t.Fatal("export differs from preview")
	}
	b.download.OnTapped()
	w = &writeFile{fail: true}
	(*save)(w, nil)
	if !strings.Contains(b.status.Text, "неполным") {
		t.Fatal(b.status.Text)
	}
}
