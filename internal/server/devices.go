package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// DeviceStore is deliberately separate from browser storage: desktop URLs use
// a random port and the Windows browser profile is recreated on every launch.
// It stores no credential fields and never talks to a router.
type DeviceStore struct {
	path string
	mu   sync.Mutex
}

func NewDeviceStore(path string) *DeviceStore { return &DeviceStore{path: path} }

type savedDevice struct {
	Name string `json:"name"`
	Host string `json:"host"`
	Note string `json:"note"`
}
type deviceDocument struct {
	Schema  int           `json:"schema"`
	Devices []savedDevice `json:"devices"`
}
type deviceSaveRequest struct {
	Revision string        `json:"revision"`
	Devices  []savedDevice `json:"devices"`
}

const deviceMaxBytes = 512 * 1024

func decodeDeviceJSON(b []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}
func validDevices(ds []savedDevice) bool {
	if ds == nil || len(ds) > 100 {
		return false
	}
	names := make(map[string]bool, len(ds))
	for _, d := range ds {
		if !utf8.ValidString(d.Name) || !utf8.ValidString(d.Host) || !utf8.ValidString(d.Note) ||
			len(d.Name) == 0 || len(d.Name) > 240 || len(d.Host) == 0 || len(d.Host) > 255 || len(d.Note) > 2000 ||
			strings.TrimSpace(d.Name) != d.Name || strings.TrimSpace(d.Host) != d.Host {
			return false
		}
		for _, r := range d.Name + d.Host + d.Note {
			if unicode.IsControl(r) {
				return false
			}
		}
		if strings.ContainsAny(d.Host, " \t\r\n/@?#\\") {
			return false
		}
		key := strings.ToLower(d.Name)
		if names[key] {
			return false
		}
		names[key] = true
	}
	return true
}
func revision(data []byte) string { h := sha256.Sum256(data); return hex.EncodeToString(h[:]) }
func (s *DeviceStore) read() (deviceDocument, string, error) {
	doc := deviceDocument{Schema: 1, Devices: []savedDevice{}}
	f, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		data, _ := json.Marshal(doc)
		return doc, revision(data), nil
	}
	if err != nil {
		return doc, "", err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, deviceMaxBytes+1))
	if err != nil || len(data) > deviceMaxBytes {
		return doc, "", errors.New("cannot read device list")
	}
	doc = deviceDocument{} // Missing schema or devices is corrupt, not an empty list.
	if err = decodeDeviceJSON(data, &doc); err != nil || doc.Schema != 1 || !validDevices(doc.Devices) {
		return doc, "", errors.New("invalid device list")
	}
	return doc, revision(data), nil
}
func (s *DeviceStore) write(ds []savedDevice) (string, error) {
	data, err := json.Marshal(deviceDocument{Schema: 1, Devices: ds})
	if err != nil {
		return "", err
	}
	if len(data) > deviceMaxBytes {
		return "", errors.New("device list exceeds size limit")
	}
	if err = os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(filepath.Dir(s.path), ".devices-*.tmp")
	if err != nil {
		return "", err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if e := f.Close(); err == nil {
		err = e
	}
	if err != nil {
		return "", err
	}
	if err = os.Rename(tmp, s.path); err != nil {
		return "", err
	}
	return revision(data), nil
}
func handleDevices(store *DeviceStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		ip := net.ParseIP(host)
		if err != nil || ip == nil || !ip.IsLoopback() {
			writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "Локальный список доступен только с этого компьютера."})
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			w.Header().Set("Allow", "GET, POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if store == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "Локальный список устройств недоступен на этой платформе."})
			return
		}
		store.mu.Lock()
		defer store.mu.Unlock()
		current, rev, err := store.read()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Не удалось прочитать список устройств. Существующий файл не изменён."})
			return
		}
		if r.Method == http.MethodGet {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "revision": rev, "devices": current.Devices})
			return
		}
		data, err := io.ReadAll(io.LimitReader(r.Body, deviceMaxBytes+1))
		var req deviceSaveRequest
		if err != nil || len(data) > deviceMaxBytes || decodeDeviceJSON(data, &req) != nil || !validDevices(req.Devices) || len(req.Revision) != 64 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "Некорректный список: допустимы только название, адрес и заметка; названия должны быть уникальными."})
			return
		}
		if req.Revision != rev {
			writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": "Список изменился в другом окне. Обновите его и повторите действие."})
			return
		}
		newRev, err := store.write(req.Devices)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Не удалось сохранить список устройств."})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "revision": newRev, "devices": req.Devices})
	}
}

// SavedDevice contains only user-entered metadata, never credentials.
// Free text may still contain secrets if the user enters them.
type SavedDevice = savedDevice

var ErrDeviceConflict = errors.New("device list revision conflict")
var ErrInvalidDevices = errors.New("invalid device list")

// Load returns a detached copy and a revision for optimistic concurrency.
func (s *DeviceStore) Load() ([]SavedDevice, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, rev, err := s.read()
	return doc.Devices, rev, err
}

// Save rejects corrupt stores, invalid data and stale revisions. The lock is
// local to this store instance; other processes can still race the final write.
func (s *DeviceStore) Save(expected string, devices []SavedDevice) (string, error) {
	if !validDevices(devices) || len(expected) != 64 {
		return "", ErrInvalidDevices
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, rev, err := s.read()
	if err != nil {
		return "", err
	}
	if rev != expected {
		return "", ErrDeviceConflict
	}
	return s.write(devices)
}

// DecodeDeviceExport accepts the same schema as the web import, with no extra
// fields or missing/null fields. The caller still validates the combined list.
func DecodeDeviceExport(data []byte) ([]SavedDevice, error) {
	if len(data) > deviceMaxBytes || !utf8.Valid(data) {
		return nil, ErrInvalidDevices
	}
	var top map[string]json.RawMessage
	if decodeDeviceJSON(data, &top) != nil || len(top) != 2 || top["schema"] == nil || top["devices"] == nil {
		return nil, ErrInvalidDevices
	}
	var schema int
	var rows []json.RawMessage
	if json.Unmarshal(top["schema"], &schema) != nil || schema != 1 || json.Unmarshal(top["devices"], &rows) != nil || rows == nil || len(rows) > 100 {
		return nil, ErrInvalidDevices
	}
	out := make([]SavedDevice, 0, len(rows))
	for _, raw := range rows {
		var obj map[string]json.RawMessage
		if json.Unmarshal(raw, &obj) != nil || len(obj) != 3 || obj["name"] == nil || obj["host"] == nil || obj["note"] == nil {
			return nil, ErrInvalidDevices
		}
		for _, k := range []string{"name", "host", "note"} {
			if bytes.Equal(bytes.TrimSpace(obj[k]), []byte("null")) {
				return nil, ErrInvalidDevices
			}
		}
		var d SavedDevice
		if decodeDeviceJSON(raw, &d) != nil {
			return nil, ErrInvalidDevices
		}
		out = append(out, d)
	}
	if !validDevices(out) {
		return nil, ErrInvalidDevices
	}
	return out, nil
}

// EncodeDeviceExport produces a web-compatible unencrypted document.
func EncodeDeviceExport(devices []SavedDevice) ([]byte, error) {
	if !validDevices(devices) {
		return nil, ErrInvalidDevices
	}
	return json.MarshalIndent(deviceDocument{Schema: 1, Devices: devices}, "", "  ")
}
