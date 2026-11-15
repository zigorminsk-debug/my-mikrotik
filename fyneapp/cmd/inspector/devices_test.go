package main

import (
	"bytes"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
	"github.com/zigorminsk-debug/my-mikrotik/internal/server"
)

type readFile struct{ io.Reader }

func (readFile) URI() fyne.URI { return nil }
func (readFile) Close() error  { return nil }

type writeFile struct {
	bytes.Buffer
	closed bool
	fail   bool
}

func (*writeFile) URI() fyne.URI { return nil }
func (w *writeFile) Close() error {
	w.closed = true
	if w.fail {
		return errors.New("failed")
	}
	return nil
}
func TestDevicesPanelCRUDImportExport(t *testing.T) {
	a := test.NewApp()
	defer a.Quit()
	store := server.NewDeviceStore(filepath.Join(t.TempDir(), "devices.json"))
	var confirm func(bool)
	var open func(fyne.URIReadCloser, error)
	var save func(fyne.URIWriteCloser, error)
	used := ""
	d := newDevicesPanel(store, func(s string) bool { used = s; return true }, func(_, _ string, cb func(bool)) { confirm = cb }, func(cb func(fyne.URIReadCloser, error)) { open = cb }, func(cb func(fyne.URIWriteCloser, error)) { save = cb })
	d.name.SetText("Office")
	d.host.SetText("192.0.2.1")
	d.note.SetText("private")
	d.add.OnTapped()
	if len(d.entries) != 1 {
		t.Fatal(d.status.Text)
	}
	d.name.SetText("office")
	d.add.OnTapped()
	if len(d.entries) != 1 || !strings.Contains(d.status.Text, "Некорректный") {
		t.Fatal(d.status.Text)
	}
	d.selectDevice.SetSelected("Office")
	d.use.OnTapped()
	if used != "192.0.2.1" {
		t.Fatal(used)
	}
	d.note.SetText("changed")
	d.update.OnTapped()
	if d.entries[0].Note != "changed" || d.selected != -1 {
		t.Fatal(d.entries)
	}
	d.exportButton.OnTapped()
	w := &writeFile{}
	save(w, nil)
	if !w.closed || !strings.Contains(w.String(), `"schema": 1`) {
		t.Fatal(w)
	}
	d.importButton.OnTapped()
	open(readFile{strings.NewReader(`{"schema":1,"devices":[{"name":"second","host":"AA:BB:CC:DD:EE:FF","note":""}]}`)}, nil)
	if len(d.entries) != 2 {
		t.Fatal(d.status.Text)
	}
	d.importButton.OnTapped()
	open(readFile{strings.NewReader(`{"schema":1,"devices":[{"name":"x","host":"x","note":"","password":"secret"}]}`)}, nil)
	if len(d.entries) != 2 || !strings.Contains(d.status.Text, "Некорректный") {
		t.Fatal(d.status.Text)
	}
	d.selectDevice.SetSelected("second")
	d.remove.OnTapped()
	confirm(false)
	if len(d.entries) != 2 {
		t.Fatal("cancel removed")
	}
	d.remove.OnTapped()
	confirm(true)
	if len(d.entries) != 1 {
		t.Fatal(d.entries)
	}
}
func TestDevicesPanelConflictAndFileErrors(t *testing.T) {
	a := test.NewApp()
	defer a.Quit()
	path := filepath.Join(t.TempDir(), "devices.json")
	s := server.NewDeviceStore(path)
	var open func(fyne.URIReadCloser, error)
	var save func(fyne.URIWriteCloser, error)
	d := newDevicesPanel(s, func(string) bool { return false }, func(_, _ string, _ func(bool)) {}, func(cb func(fyne.URIReadCloser, error)) { open = cb }, func(cb func(fyne.URIWriteCloser, error)) { save = cb })
	d.name.SetText("one")
	d.host.SetText("a")
	d.add.OnTapped()
	d.selectDevice.SetSelected("one")
	d.use.OnTapped()
	if !strings.Contains(d.status.Text, "Дождитесь") {
		t.Fatal(d.status.Text)
	}
	d.importButton.OnTapped()
	open(readFile{strings.NewReader(strings.Repeat("x", 512*1024+1))}, nil)
	if len(d.entries) != 1 {
		t.Fatal("oversize imported")
	}
	d.exportButton.OnTapped()
	w := &writeFile{fail: true}
	save(w, nil)
	if !w.closed || !strings.Contains(d.status.Text, "неполным") {
		t.Fatal(d.status.Text)
	}
	other := server.NewDeviceStore(path)
	_, rev, _ := other.Load()
	_, err := other.Save(rev, []server.SavedDevice{{Name: "external", Host: "b"}})
	if err != nil {
		t.Fatal(err)
	}
	d.name.SetText("two")
	d.add.OnTapped()
	if d.revision != "" || !strings.Contains(d.status.Text, "другом окне") {
		t.Fatal(d.status.Text)
	}
	d.reload()
	if len(d.entries) != 1 || d.entries[0].Name != "external" {
		t.Fatal(d.entries)
	}
}
func TestFillSavedDeviceResetsSecretsAndState(t *testing.T) {
	a := test.NewApp()
	defer a.Quit()
	host := widget.NewEntry()
	password := widget.NewPasswordEntry()
	protocol := widget.NewSelect([]string{"auto", "api", "rest", "mac"}, nil)
	protocol.SetSelected("rest")
	previous := "auto"
	count := 0
	host.SetText("192.0.2.1")
	password.SetText("secret")
	fillSavedDevice("192.0.2.1", host, password, protocol, &previous, func() { count++ })
	if password.Text != "" || count != 1 || protocol.Selected != "rest" {
		t.Fatal(password.Text, count, protocol.Selected)
	}
	fillSavedDevice("AA:BB:CC:DD:EE:FF", host, password, protocol, &previous, func() { count++ })
	if protocol.Selected != "mac" || previous != "rest" {
		t.Fatal(protocol.Selected, previous)
	}
	fillSavedDevice("192.0.2.2", host, password, protocol, &previous, func() { count++ })
	if protocol.Selected != "rest" || count != 3 {
		t.Fatal(protocol.Selected, count)
	}
}
