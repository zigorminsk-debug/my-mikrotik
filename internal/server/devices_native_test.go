package server

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeDeviceStoreAndWebInterop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devices.json")
	s := NewDeviceStore(path)
	empty, rev, err := s.Load()
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatal(empty, err)
	}
	list := []SavedDevice{{Name: "Офис", Host: "192.0.2.1", Note: "основной"}}
	newRev, err := s.Save(rev, list)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Save(rev, []SavedDevice{}); !errors.Is(err, ErrDeviceConflict) {
		t.Fatal(err)
	}
	h := handleDevices(s)
	status, response := deviceRequest(t, h, "GET", "")
	if status != 200 || response.Revision != newRev || response.Devices[0] != list[0] {
		t.Fatal(status, response)
	}
	status, response = deviceRequest(t, h, "POST", saveBody(newRev, []savedDevice{{Name: "other", Host: "host"}}))
	if status != 200 {
		t.Fatal(response)
	}
	if _, err = s.Save(newRev, list); !errors.Is(err, ErrDeviceConflict) {
		t.Fatal(err)
	}
	if _, err = s.Save(response.Revision, []SavedDevice{{Name: "x", Host: "x"}, {Name: "X", Host: "y"}}); !errors.Is(err, ErrInvalidDevices) {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if bytes.Contains(raw, []byte("password")) {
		t.Fatal("credential field")
	}
	if err = os.WriteFile(path, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Save(response.Revision, list); err == nil {
		t.Fatal("corrupt store overwritten")
	}
	raw, _ = os.ReadFile(path)
	if string(raw) != "broken" {
		t.Fatal(string(raw))
	}
}
func TestDeviceExportStrictAndRoundTrip(t *testing.T) {
	list := []SavedDevice{{Name: "one", Host: "192.0.2.1", Note: ""}}
	b, err := EncodeDeviceExport(list)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeDeviceExport(b)
	if err != nil || len(got) != 1 || got[0] != list[0] {
		t.Fatal(got, err)
	}
	invalid := []string{`{"schema":1,"devices":[{"name":"x","host":"a"}]}`, `{"schema":1,"devices":[{"name":"x","host":"a","note":null}]}`, `{"schema":1,"devices":[{"name":"x","host":"a","note":"","password":"x"}]}`, `{"schema":1,"devices":null}`, `{"schema":1,"devices":[],"extra":1}`, `{"schema":1,"devices":[{"name":"x","host":"a","note":""},{"name":"X","host":"b","note":""}]}`, `{"schema":2,"devices":[]}`, `{"schema":1,"devices":[]} {}`, strings.Repeat(" ", 512*1024+1), string([]byte{0xff})}
	for _, s := range invalid {
		if _, err := DecodeDeviceExport([]byte(s)); err == nil {
			t.Fatalf("accepted %q", s[:min(len(s), 100)])
		}
	}
	if _, err := EncodeDeviceExport(nil); err == nil {
		t.Fatal("accepted nil")
	}
}
