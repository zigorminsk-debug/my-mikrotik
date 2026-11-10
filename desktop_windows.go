//go:build windows

package main

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"

	"github.com/zigorminsk-debug/my-mikrotik/internal/server"
)

// Как долго ждём, пока окно браузера запросит страницу интерфейса: если за
// это время обращения не было, окно осталось пустым (белым) и интерфейс
// открывается в браузере по умолчанию.
const windowGrace = 20 * time.Second

// Сколько ждём появления значка в области уведомлений перед решением,
// есть ли у пользователя способ завершить программу вручную.
const trayGrace = 5 * time.Second

// Идентификатор значка в области уведомлений.
const trayUID = 1

// Пункты меню значка.
const (
	trayOpenMenu = 1001
	trayExitMenu = 1002
)

// Константы Win32, нужные значку в области уведомлений.
const (
	wmApp       = 0x8000
	wmCommand   = 0x0111
	wmDestroy   = 0x0002
	wmQuit      = 0x0012
	wmNull      = 0x0000
	wmRButtonUp = 0x0205

	nimAdd     = 0x00000000
	nimDelete  = 0x00000002
	nifMessage = 0x00000001
	nifIcon    = 0x00000002
	nifTip     = 0x00000004

	imageIcon      = 1
	lrLoadFromFile = 0x00000010
	lrDefaultSize  = 0x00000040

	mfString = 0x00000000

	tpmLeftButton  = 0x0000
	tpmRightAlign  = 0x0008
	tpmBottomAlign = 0x0020
	tpmNoNotify    = 0x0080
	tpmReturnCmd   = 0x0100

	mbOK = 0x00000000

	mbIconInfo = 0x00000040
)

// hwndMessage — (HWND)-3: окно только для приёма сообщений, без отображения.
var hwndMessage = ^uintptr(2)

// pageRequests считает обращения к странице интерфейса. Если окно браузера
// открылось, но страницу ни разу не запросили — окно осталось пустым.
var pageRequests atomic.Int64

// trayActive — значок действительно добавлен в область уведомлений и им
// можно завершить программу.
var trayActive atomic.Bool

// instrument оборачивает HTTP-обработчик, чтобы считать открытия интерфейса.
func instrument(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p := r.URL.Path; p == "/" || p == "/index.html" {
			pageRequests.Add(1)
		}
		h.ServeHTTP(w, r)
	})
}

// notifyIconData — NOTIFYICONDATAW (Windows Vista и новее).
type notifyIconData struct {
	CbSize           uint32
	HWnd             uintptr
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	HIcon            uintptr
	SzTip            [128]uint16
	DwState          uint32
	DwStateMask      uint32
	SzInfo           [256]uint16
	UVersion         uint32
	SzInfoTitle      [64]uint16
	DwInfoFlags      uint32
	GuidItem         [16]byte
	HBalloonIcon     uintptr
}

// wndClassEx — WNDCLASSEXW.
type wndClassEx struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     uintptr
	HIcon         uintptr
	HCursor       uintptr
	HbrBackground uintptr
	LpszMenuName  uintptr
	LpszClassName uintptr
	HIconSm       uintptr
}

// winMsg — MSG.
type winMsg struct {
	HWnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      point
}

// point — POINT.
type point struct {
	X int32
	Y int32
}

// trayIcon — значок программы в области уведомлений.
type trayIcon struct {
	url    string
	onExit func()
	hwnd   uintptr
	menu   uintptr
	icon   uintptr
	added  bool
}

var (
	trayMu      sync.Mutex
	trayWindows = map[uintptr]*trayIcon{}
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
		return nil, nil, fmt.Errorf("MikroTik Inspector уже запущен — значок программы в области уведомлений. " +
			"Завершите его через меню значка и запустите программу снова")
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

	quit := make(chan struct{})
	var quitOnce sync.Once
	// Значок в области уведомлений: индикатор работы и меню с выходом.
	// Он не может помешать запуску — при ошибке работаем без значка.
	stopTray := startTray(url, func() { quitOnce.Do(func() { close(quit) }) })

	done := make(chan error, 1)
	edgeExited := make(chan error, 1)
	go func() {
		edgeExited <- cmd.Wait()
	}()
	go func() {
		var edgeErr error
		exited, quitting := false, false
		deadline := time.Now().Add(windowGrace)
		// Ждём первое обращение к странице: если его нет, окно пустое.
		for !exited && !quitting && time.Now().Before(deadline) && pageRequests.Load() == 0 {
			select {
			case err := <-edgeExited:
				edgeErr, exited = err, true
			case <-quit:
				quitting = true
			case <-time.After(300 * time.Millisecond):
			}
		}
		if !quitting && pageRequests.Load() == 0 {
			// Окно открылось, но страницу не показало: открываем интерфейс
			// в браузере по умолчанию и ждём команды из меню значка.
			log.Printf("окно Edge не открыло страницу за %s — открываем браузер по умолчанию: %s", windowGrace, url)
			openBrowser(url)
			notifyFallback(url)
			// Без значка в области уведомлений завершить программу будет
			// нечем: не остаёмся «призраком» без управления.
			waitTray(trayGrace)
			if trayActive.Load() {
				select {
				case err := <-edgeExited:
					edgeErr = err
				case <-quit:
				}
			} else {
				log.Printf("значок в области уведомлений недоступен — завершаем работу")
			}
		} else if !exited && !quitting {
			select {
			case err := <-edgeExited:
				edgeErr = err
			case <-quit:
			}
		}
		log.Printf("Завершение: окно закрыто=%v, выход из меню=%v", exited, quitting)
		if edgeErr != nil {
			log.Printf("Окно Edge завершилось с ошибкой: %v", edgeErr)
		}
		if e := os.RemoveAll(profile); e != nil {
			log.Printf("Не удалось удалить временный профиль Edge: %v", e)
		}
		stopTray()
		release()
		if edgeErr != nil {
			done <- fmt.Errorf("окно Microsoft Edge завершилось с ошибкой: %w", edgeErr)
		} else {
			done <- nil
		}
	}()
	return done, func() { _ = cmd.Process.Kill() }, nil
}

// startTray показывает значок в области уведомлений, пока работает программа.
// Возвращает функцию, которая убирает значок при завершении.
func startTray(url string, onExit func()) func() {
	stopped := make(chan struct{})
	var threadID atomic.Uint32
	go func() {
		// Окно и цикл сообщений обязаны жить в одном потоке.
		runtime.LockOSThread()
		defer close(stopped)
		tray, err := newTrayIcon(url, onExit)
		if err != nil {
			log.Printf("значок в области уведомлений не создан: %v", err)
			return
		}
		threadID.Store(uint32(getCurrentThreadID()))
		defer tray.destroy()
		tray.run()
	}()
	return func() {
		if tid := threadID.Load(); tid != 0 {
			postThreadMessage(uintptr(tid), wmQuit, 0, 0)
		}
		select {
		case <-stopped:
		case <-time.After(3 * time.Second):
		}
	}
}

// waitTray ждёт, пока значок в области уведомлений появится.
func waitTray(timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for !trayActive.Load() && time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
	}
}

// newTrayIcon создаёт скрытое окно и добавляет значок в область уведомлений.
func newTrayIcon(url string, onExit func()) (*trayIcon, error) {
	user32 := syscall.NewLazyDLL("user32.dll")
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	shell32 := syscall.NewLazyDLL("shell32.dll")
	inst, _, err := kernel32.NewProc("GetModuleHandleW").Call(0)
	if inst == 0 {
		return nil, fmt.Errorf("GetModuleHandleW: %v", err)
	}
	icon, err := loadTrayIcon()
	if err != nil {
		return nil, err
	}
	t := &trayIcon{
		url:    url,
		onExit: onExit,
		icon:   icon,
	}
	className, err := syscall.UTF16PtrFromString("MikroTikInspectorTray")
	if err != nil {
		t.destroy()
		return nil, err
	}
	wc := wndClassEx{
		CbSize:        uint32(unsafe.Sizeof(wndClassEx{})),
		LpfnWndProc:   syscall.NewCallback(trayWndProc),
		HInstance:     inst,
		LpszClassName: uintptr(unsafe.Pointer(className)),
	}
	if atom, _, e := user32.NewProc("RegisterClassExW").Call(uintptr(unsafe.Pointer(&wc))); atom == 0 {
		t.destroy()
		return nil, fmt.Errorf("RegisterClassExW: %v", e)
	}
	title, err := syscall.UTF16PtrFromString("MikroTik Inspector")
	if err != nil {
		t.destroy()
		return nil, err
	}
	hwnd, _, e := user32.NewProc("CreateWindowExW").Call(
		0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(title)),
		0,
		0, 0, 0, 0,
		hwndMessage,
		0,
		inst,
		0,
	)
	if hwnd == 0 {
		t.destroy()
		return nil, fmt.Errorf("CreateWindowExW: %v", e)
	}
	t.hwnd = hwnd
	menu, _, e := user32.NewProc("CreatePopupMenu").Call()
	if menu == 0 {
		t.destroy()
		return nil, fmt.Errorf("CreatePopupMenu: %v", e)
	}
	t.menu = menu
	appendMenu(menu, mfString, trayOpenMenu, "Открыть интерфейс")
	appendMenu(menu, mfString, trayExitMenu, "Выход")

	nidSize := uint32(unsafe.Sizeof(notifyIconData{}))
	tip := "MikroTik Inspector — " + url
	nid := notifyIconData{
		CbSize:           nidSize,
		HWnd:             hwnd,
		UID:              trayUID,
		UFlags:           nifMessage | nifIcon | nifTip,
		UCallbackMessage: wmApp,
		HIcon:            icon,
	}
	copy(nid.SzTip[:], utf16.Encode([]rune(tip)))
	if ok, _, e := shell32.NewProc("Shell_NotifyIconW").Call(nimAdd, uintptr(unsafe.Pointer(&nid))); ok == 0 {
		t.destroy()
		return nil, fmt.Errorf("Shell_NotifyIconW: %v", e)
	}
	t.added = true
	trayActive.Store(true)
	trayMu.Lock()
	trayWindows[hwnd] = t
	trayMu.Unlock()
	log.Printf("Значок в области уведомлений создан: %s", url)
	return t, nil
}

// run крутит цикл сообщений скрытого окна, пока не придёт WM_QUIT.
func (t *trayIcon) run() {
	user32 := syscall.NewLazyDLL("user32.dll")
	getMessage := user32.NewProc("GetMessageW")
	translate := user32.NewProc("TranslateMessage")
	dispatch := user32.NewProc("DispatchMessageW")
	var m winMsg
	for {
		ret, _, _ := getMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(ret) <= 0 {
			return
		}
		translate.Call(uintptr(unsafe.Pointer(&m)))
		dispatch.Call(uintptr(unsafe.Pointer(&m)))
	}
}

// showMenu показывает меню значка на месте курсора.
func (t *trayIcon) showMenu() {
	user32 := syscall.NewLazyDLL("user32.dll")
	user32.NewProc("SetForegroundWindow").Call(t.hwnd)
	var pt point
	user32.NewProc("GetCursorPos").Call(uintptr(unsafe.Pointer(&pt)))
	flags := uintptr(tpmLeftButton | tpmRightAlign | tpmBottomAlign | tpmNoNotify | tpmReturnCmd)
	user32.NewProc("TrackPopupMenu").Call(t.menu, flags, uintptr(int(pt.X)), uintptr(int(pt.Y)), 0, t.hwnd, 0)
	user32.NewProc("PostMessageW").Call(t.hwnd, wmNull, 0, 0)
}

// destroy убирает значок и освобождает ресурсы Win32.
func (t *trayIcon) destroy() {
	user32 := syscall.NewLazyDLL("user32.dll")
	shell32 := syscall.NewLazyDLL("shell32.dll")
	if t.added {
		nid := notifyIconData{
			CbSize: uint32(unsafe.Sizeof(notifyIconData{})),
			HWnd:   t.hwnd,
			UID:    trayUID,
		}
		shell32.NewProc("Shell_NotifyIconW").Call(nimDelete, uintptr(unsafe.Pointer(&nid)))
		t.added = false
		trayActive.Store(false)
	}
	if t.menu != 0 {
		user32.NewProc("DestroyMenu").Call(t.menu)
		t.menu = 0
	}
	if t.icon != 0 {
		user32.NewProc("DestroyIcon").Call(t.icon)
		t.icon = 0
	}
	if t.hwnd != 0 {
		user32.NewProc("DestroyWindow").Call(t.hwnd)
		trayMu.Lock()
		delete(trayWindows, t.hwnd)
		trayMu.Unlock()
		t.hwnd = 0
	}
}

// trayWndProc — оконная процедура скрытого окна значка.
func trayWndProc(hwnd uintptr, msg uint32, wparam, lparam uintptr) uintptr {
	switch msg {
	case wmApp:
		if uint32(lparam) == wmRButtonUp {
			if t := trayLookup(hwnd); t != nil {
				t.showMenu()
			}
		}
		return 0
	case wmCommand:
		if t := trayLookup(hwnd); t != nil {
			switch wparam & 0xffff {
			case trayOpenMenu:
				openBrowser(t.url)
			case trayExitMenu:
				t.onExit()
			}
		}
		return 0
	case wmDestroy:
		syscall.NewLazyDLL("user32.dll").NewProc("PostQuitMessage").Call(0)
		return 0
	}
	return defWindowProc(hwnd, msg, wparam, lparam)
}

func trayLookup(hwnd uintptr) *trayIcon {
	trayMu.Lock()
	defer trayMu.Unlock()
	return trayWindows[hwnd]
}

func defWindowProc(hwnd uintptr, msg uint32, wparam, lparam uintptr) uintptr {
	ret, _, _ := syscall.NewLazyDLL("user32.dll").NewProc("DefWindowProcW").Call(hwnd, uintptr(msg), wparam, lparam)
	return ret
}

func appendMenu(menu uintptr, flags, id uint32, text string) {
	textPtr, err := syscall.UTF16PtrFromString(text)
	if err != nil {
		log.Printf("меню значка: не удалось преобразовать пункт: %v", err)
		return
	}
	syscall.NewLazyDLL("user32.dll").NewProc("AppendMenuW").Call(menu, uintptr(flags), uintptr(id), uintptr(unsafe.Pointer(textPtr)))
}

// loadTrayIcon записывает .ico в профиль пользователя (один раз) и загружает
// его как HICON. Иконка рисуется той же процедурой, что и /icon.png.
func loadTrayIcon() (uintptr, error) {
	dir := appDir()
	if dir == "" {
		return 0, errors.New("не найден каталог LOCALAPPDATA")
	}
	path := filepath.Join(dir, "app.ico")
	if _, err := os.Stat(path); err != nil {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return 0, err
		}
		if err := os.WriteFile(path, server.IconICO(), 0600); err != nil {
			return 0, err
		}
	}
	pathPtr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	icon, _, err := syscall.NewLazyDLL("user32.dll").NewProc("LoadImageW").Call(
		0,
		uintptr(unsafe.Pointer(pathPtr)),
		imageIcon,
		0,
		0,
		lrLoadFromFile|lrDefaultSize,
	)
	if icon == 0 {
		return 0, fmt.Errorf("LoadImageW %s: %v", path, err)
	}
	return icon, nil
}

func getCurrentThreadID() uintptr {
	tid, _, _ := syscall.NewLazyDLL("kernel32.dll").NewProc("GetCurrentThreadId").Call()
	return tid
}

func postThreadMessage(threadID, msg, wparam, lparam uintptr) {
	syscall.NewLazyDLL("kernel32.dll").NewProc("PostThreadMessageW").Call(threadID, msg, wparam, lparam)
}

// notifyFallback объясняет, почему интерфейс открыт в обычном браузере.
func notifyFallback(url string) {
	user32 := syscall.NewLazyDLL("user32.dll")
	title, _ := syscall.UTF16PtrFromString("MikroTik Inspector")

	text, _ := syscall.UTF16PtrFromString(
		"Не удалось открыть окно Microsoft Edge — страница интерфейса не загрузилась.\n\n" +
			"Интерфейс открыт в браузере по умолчанию:\n" + url +
			"\n\nПрограмма работает, пока вы не выберете «Выход» в меню значка " +
			"в области уведомлений.")
	user32.NewProc("MessageBoxW").Call(
		0,
		uintptr(unsafe.Pointer(text)),
		uintptr(unsafe.Pointer(title)),
		mbIconInfo|mbOK,
	)
}

func showStartupError(err error) {
	title, _ := syscall.UTF16PtrFromString("MikroTik Inspector")
	message, _ := syscall.UTF16PtrFromString(err.Error())
	syscall.NewLazyDLL("user32.dll").NewProc("MessageBoxW").Call(0, uintptr(unsafe.Pointer(message)), uintptr(unsafe.Pointer(title)), 0x10)
}

// appDir — каталог программы в профиле пользователя: журнал, кэш иконки,
// временные профили Edge.
func appDir() string {
	root := os.Getenv("LOCALAPPDATA")
	if root == "" {
		return ""
	}
	return filepath.Join(root, "MikroTik Inspector")
}

// The GUI subsystem has no console; keep startup diagnostics in a local file.
func initLogging() func() {
	dir := appDir()
	if dir == "" {
		return func() {}
	}
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
