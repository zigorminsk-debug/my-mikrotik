package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type deviceTestResponse struct {
	OK       bool          `json:"ok"`
	Error    string        `json:"error"`
	Revision string        `json:"revision"`
	Devices  []savedDevice `json:"devices"`
}

func deviceRequest(t *testing.T, h http.Handler, method, body string) (int, deviceTestResponse) {
	t.Helper()
	r := httptest.NewRequest(method, "http://127.0.0.1/api/devices", strings.NewReader(body))
	r.RemoteAddr = "127.0.0.1:12345"
	if method == http.MethodPost {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var result deviceTestResponse
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("response %d: %s", w.Code, w.Body.String())
	}
	return w.Code, result
}
func saveBody(rev string, devices any) string {
	b, _ := json.Marshal(map[string]any{"revision": rev, "devices": devices})
	return string(b)
}
func TestDeviceStorePersistenceAndConflict(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", "devices.json")
	store := NewDeviceStore(path)
	h := withSecurityHeaders(handleDevices(store))
	status, first := deviceRequest(t, h, "GET", "")
	if status != 200 || !first.OK || len(first.Devices) != 0 || len(first.Revision) != 64 {
		t.Fatal(status, first)
	}
	devices := []savedDevice{{Name: "Офис", Host: "192.168.88.5", Note: "Тестовый"}, {Name: "MAC", Host: "AA:BB:CC:DD:EE:FF"}}
	status, saved := deviceRequest(t, h, "POST", saveBody(first.Revision, devices))
	if status != 200 || !saved.OK || saved.Revision == first.Revision {
		t.Fatal(status, saved)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("password")) {
		t.Fatal("credential field written")
	}
	// A fresh store instance and fresh server still see the same file.
	h2 := withSecurityHeaders(handleDevices(NewDeviceStore(path)))
	status, reloaded := deviceRequest(t, h2, "GET", "")
	if status != 200 || reloaded.Revision != saved.Revision || len(reloaded.Devices) != 2 || reloaded.Devices[0] != devices[0] {
		t.Fatal(status, reloaded)
	}
	status, _ = deviceRequest(t, h, "POST", saveBody(first.Revision, []savedDevice{}))
	if status != http.StatusConflict {
		t.Fatalf("stale write: %d", status)
	}
	status, removed := deviceRequest(t, h, "POST", saveBody(saved.Revision, []savedDevice{devices[1]}))
	if status != 200 || len(removed.Devices) != 1 {
		t.Fatal(status, removed)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("temporary file left behind: %v", entries)
	}
}
func TestDeviceStoreRejectsInvalidRequestsWithoutOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devices.json")
	h := withSecurityHeaders(handleDevices(NewDeviceStore(path)))
	_, initial := deviceRequest(t, h, "GET", "")
	valid := savedDevice{Name: "router", Host: "192.168.88.1"}
	_, saved := deviceRequest(t, h, "POST", saveBody(initial.Revision, []savedDevice{valid}))
	before, _ := os.ReadFile(path)
	invalid := []string{
		fmt.Sprintf(`{"revision":%q,"devices":[{"name":"x","host":"192.0.2.1","note":"","password":"secret"}]}`, saved.Revision),
		saveBody(saved.Revision, []savedDevice{{Name: "same", Host: "one"}, {Name: "SAME", Host: "two"}}),
		saveBody(saved.Revision, []savedDevice{{Name: "", Host: "a"}}),
		saveBody(saved.Revision, []savedDevice{{Name: "a", Host: "https://admin:secret@host/"}}),
		saveBody(saved.Revision, []savedDevice{{Name: "a", Host: "host", Note: "line\nbreak"}}),
		saveBody(saved.Revision, make([]savedDevice, 101)),
		saveBody(saved.Revision, nil),
		saveBody(saved.Revision, []savedDevice{valid}) + `{}`,
		`{"revision":"x","devices":[]}`,
		strings.Repeat(" ", deviceMaxBytes+1),
	}
	for i, body := range invalid {
		status, _ := deviceRequest(t, h, "POST", body)
		if status != 400 {
			t.Errorf("case %d status %d", i, status)
		}
		after, _ := os.ReadFile(path)
		if !bytes.Equal(before, after) {
			t.Fatalf("case %d modified store", i)
		}
	}
	req := httptest.NewRequest("POST", "http://127.0.0.1/api/devices", strings.NewReader(saveBody(saved.Revision, []savedDevice{})))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://evil.example")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
}
func TestDeviceStoreConcurrentUpdates(t *testing.T) {
	h := withSecurityHeaders(handleDevices(NewDeviceStore(filepath.Join(t.TempDir(), "devices.json"))))
	_, start := deviceRequest(t, h, "GET", "")
	var wg sync.WaitGroup
	results := make(chan int, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body := saveBody(start.Revision, []savedDevice{{Name: fmt.Sprint(i), Host: "host"}})
			r := httptest.NewRequest("POST", "http://127.0.0.1/api/devices", strings.NewReader(body))
			r.RemoteAddr = "127.0.0.1:12345"
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			results <- w.Code
		}(i)
	}
	wg.Wait()
	close(results)
	ok, conflict := 0, 0
	for result := range results {
		if result == 200 {
			ok++
		} else if result == 409 {
			conflict++
		} else {
			t.Fatal(result)
		}
	}
	if ok != 1 || conflict != 19 {
		t.Fatal(ok, conflict)
	}
}
func TestDeviceStoreCorruptOrUnavailableDoesNotOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devices.json")
	raw := []byte(`{"schema":9,"devices":[]}`)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	h := withSecurityHeaders(handleDevices(NewDeviceStore(path)))
	status, _ := deviceRequest(t, h, "GET", "")
	if status != 500 {
		t.Fatal(status)
	}
	status, _ = deviceRequest(t, h, "POST", saveBody(strings.Repeat("0", 64), []savedDevice{}))
	if status != 500 {
		t.Fatal(status)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(after, raw) {
		t.Fatal("corrupt file overwritten")
	}
	status, _ = deviceRequest(t, withSecurityHeaders(handleDevices(nil)), "GET", "")
	if status != 503 {
		t.Fatal(status)
	}
	// A file in place of the directory must produce an error, not a success.
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	broken := NewDeviceStore(filepath.Join(blocker, "devices.json"))
	if _, err := broken.write([]savedDevice{}); err == nil {
		t.Fatal("write unexpectedly succeeded")
	}
}

func TestDeviceStoreRejectsRemoteClient(t *testing.T) {
	h := withSecurityHeaders(handleDevices(NewDeviceStore(filepath.Join(t.TempDir(), "devices.json"))))
	for _, addr := range []string{"192.0.2.1:9000", "[2001:db8::1]:9000", "bad"} {
		req := httptest.NewRequest("GET", "http://localhost/api/devices", nil)
		req.RemoteAddr = addr
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s: %d", addr, w.Code)
		}
	}
	req := httptest.NewRequest("GET", "http://localhost/api/devices", nil)
	req.RemoteAddr = "[::1]:9000"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatal(w.Code)
	}
}
