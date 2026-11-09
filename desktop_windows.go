//go:build windows

package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"unsafe"
)

// A dedicated Edge profile prevents reuse of the user's browser process. A
// named mutex ensures another Inspector instance cannot reuse this profile.
func openDesktop(url string) (<-chan error, func(), error) {
	mutexName, _ := syscall.UTF16PtrFromString("Local\\MikroTikInspectorWindow")
	handle, _, callErr := syscall.NewLazyDLL("kernel32.dll").NewProc("CreateMutexW").Call(0, 0, uintptr(unsafe.Pointer(mutexName)))
	if handle == 0 {
		return nil, nil, fmt.Errorf("не удалось создать блокировку приложения: %v", callErr)
	}
	if callErr == syscall.Errno(183) {
		syscall.CloseHandle(syscall.Handle(handle))
		return nil, nil, fmt.Errorf("MikroTik Inspector уже запущен")
	}
	release := func() { _ = syscall.CloseHandle(syscall.Handle(handle)) }
	var edge string
	if p, err := exec.LookPath("msedge.exe"); err == nil {
		edge = p
	}
	if edge == "" {
		for _, root := range []string{os.Getenv("PROGRAMFILES(X86)"), os.Getenv("PROGRAMFILES"), os.Getenv("LOCALAPPDATA")} {
			if root == "" {
				continue
			}
			path := filepath.Join(root, "Microsoft", "Edge", "Application", "msedge.exe")
			if info, err := os.Stat(path); err == nil && !info.IsDir() {
				edge = path
				break
			}
		}
	}
	if edge == "" {
		release()
		return nil, nil, fmt.Errorf("не найден Microsoft Edge. Установите Edge и повторите запуск")
	}
	root := os.Getenv("LOCALAPPDATA")
	if root == "" {
		release()
		return nil, nil, fmt.Errorf("не найден каталог LOCALAPPDATA")
	}
	sessions := filepath.Join(root, "MikroTik Inspector", "Sessions")
	if err := os.MkdirAll(sessions, 0700); err != nil {
		release()
		return nil, nil, err
	}
	// A fresh, exclusive profile cannot reuse a stale Edge browser process.
	// Otherwise the launcher can exit immediately and shut the server down
	// while an orphaned window is still navigating to localhost.
	profile, err := os.MkdirTemp(sessions, "session-")
	if err != nil {
		release()
		return nil, nil, err
	}
	log.Printf("Запуск окна Edge: %s; URL: %s", edge, url)
	cmd := exec.Command(edge, "--user-data-dir="+profile, "--no-first-run", "--no-default-browser-check", "--disable-background-mode", "--no-proxy-server", "--app="+url)
	if err := cmd.Start(); err != nil {
		_ = os.RemoveAll(profile)
		release()
		return nil, nil, err
	}
	done := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		log.Printf("Окно Edge завершилось: %v", err)
		if e := os.RemoveAll(profile); e != nil {
			log.Printf("Не удалось удалить временный профиль Edge: %v", e)
		}
		release()
		if err != nil {
			done <- fmt.Errorf("окно Microsoft Edge завершилось с ошибкой: %w", err)
		} else {
			done <- nil
		}
	}()
	return done, func() { _ = cmd.Process.Kill() }, nil
}

func showStartupError(err error) {
	title, _ := syscall.UTF16PtrFromString("MikroTik Inspector")
	message, _ := syscall.UTF16PtrFromString(err.Error())
	syscall.NewLazyDLL("user32.dll").NewProc("MessageBoxW").Call(0, uintptr(unsafe.Pointer(message)), uintptr(unsafe.Pointer(title)), 0x10)
}

// The GUI subsystem has no console; keep startup diagnostics in a local file.
func initLogging() func() {
	root := os.Getenv("LOCALAPPDATA")
	if root == "" {
		return func() {}
	}
	dir := filepath.Join(root, "MikroTik Inspector")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return func() {}
	}
	path := filepath.Join(dir, "inspector.log")
	if info, err := os.Stat(path); err == nil && info.Size() > 1<<20 {
		_ = os.Remove(path)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return func() {}
	}
	log.SetOutput(file)
	return func() { _ = file.Close() }
}
