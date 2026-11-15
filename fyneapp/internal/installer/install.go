// Package installer manages only the separate Fyne application's own files.
package installer

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const Executable = "MikroTikInspectorFyne.exe"
const manifestName = "fyne-install.json"
const lockName = ".fyne-install.lock"

type manifest struct {
	Format string `json:"format"`
	SHA256 string `json:"sha256"`
}

func digest(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
func checkedDir(dir string, create bool) error {
	if create {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
	}
	f, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !f.IsDir() || f.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("папка установки не является обычной папкой")
	}
	return nil
}
func regular(path string) (bool, error) {
	s, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !s.Mode().IsRegular() {
		return false, fmt.Errorf("необычный файл в папке установки: %s", filepath.Base(path))
	}
	return true, nil
}
func lock(dir string) (func(), error) {
	p := filepath.Join(dir, lockName)
	f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, fmt.Errorf("другая установка выполняется либо остался файл блокировки: %w", err)
	}
	if err = f.Close(); err != nil {
		_ = os.Remove(p)
		return nil, err
	}
	return func() { _ = os.Remove(p) }, nil
}
func existing(dir string) (bool, error) {
	exe, manifestPath := filepath.Join(dir, Executable), filepath.Join(dir, manifestName)
	hasExe, err := regular(exe)
	if err != nil {
		return false, err
	}
	hasManifest, err := regular(manifestPath)
	if err != nil {
		return false, err
	}
	if !hasExe && !hasManifest {
		return false, nil
	}
	if !hasExe || !hasManifest {
		return false, fmt.Errorf("обнаружена неполная прежняя установка; автоматически заменять файлы нельзя")
	}
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return false, err
	}
	var m manifest
	if len(raw) > 1024 || json.Unmarshal(raw, &m) != nil || m.Format != "mti-fyne-1" {
		return false, fmt.Errorf("неизвестная установка; файлы не заменены")
	}
	data, err := os.ReadFile(exe)
	if err != nil {
		return false, err
	}
	if digest(data) != m.SHA256 {
		return false, fmt.Errorf("приложение было изменено; файлы не заменены")
	}
	return true, nil
}
func writeAtomic(dir, name string, data []byte) error {
	f, err := os.CreateTemp(dir, ".fyne-staging-*")
	if err != nil {
		return err
	}
	path := f.Name()
	defer os.Remove(path)
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(path, filepath.Join(dir, name))
}

// Install never removes unrelated files, terminates processes or changes RouterOS.
// If Windows refuses to replace a running EXE, it returns an error without retry.
func Install(dir string, payload []byte) error {
	if len(payload) == 0 {
		return fmt.Errorf("пустое приложение в установщике")
	}
	if err := checkedDir(dir, true); err != nil {
		return err
	}
	unlock, err := lock(dir)
	if err != nil {
		return err
	}
	defer unlock()
	if _, err = existing(dir); err != nil {
		return err
	}
	m, _ := json.Marshal(manifest{Format: "mti-fyne-1", SHA256: digest(payload)})
	if err = writeAtomic(dir, Executable, payload); err != nil {
		return fmt.Errorf("не удалось записать приложение (закройте его перед обновлением): %w", err)
	}
	if err = writeAtomic(dir, manifestName, m); err != nil {
		return fmt.Errorf("приложение записано, но установка не завершена: %w", err)
	}
	return nil
}

// Remove deletes only the unchanged application and its manifest, not user data.
func Remove(dir string) error {
	if err := checkedDir(dir, false); err != nil {
		return err
	}
	unlock, err := lock(dir)
	if err != nil {
		return err
	}
	defer unlock()
	has, err := existing(dir)
	if err != nil {
		return err
	}
	if !has {
		return fmt.Errorf("управляемая установка не найдена")
	}
	if err = os.Remove(filepath.Join(dir, Executable)); err != nil {
		return fmt.Errorf("не удалось удалить приложение (закройте его): %w", err)
	}
	if err = os.Remove(filepath.Join(dir, manifestName)); err != nil {
		return fmt.Errorf("приложение удалено, но остался файл установки: %w", err)
	}
	return nil
}
