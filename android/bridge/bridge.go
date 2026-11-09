// Package bridge — точка входа для Android-приложения.
//
// Пакет экспортирует небольшой набор функций, которые gomobile bind
// превращает в Java-класс com.mikrotik.inspector.bridge.Bridge.
// Внутри запускается тот же HTTP-сервер, что и в настольной версии, а
// Java-обвязка (MainActivity) показывает страницу в WebView.
package bridge

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"sync"

	"github.com/zigorminsk-debug/my-mikrotik/internal/server"
	"github.com/zigorminsk-debug/my-mikrotik/web"
)

// Version подставляется при сборке через -ldflags "-X .../bridge.version=...".
var version = "dev"

var (
	mu             sync.Mutex
	started        bool
	activeServer   *http.Server
	activeListener net.Listener
)

// Start запускает локальный HTTP-сервер на 127.0.0.1:port.
// Возвращает пустую строку при успехе или текст ошибки.
func Start(port int) string {
	if port <= 0 || port > 65535 {
		port = 8765
	}
	if _, err := StartOn(net.JoinHostPort("127.0.0.1", strconv.Itoa(port))); err != nil {
		return err.Error()
	}
	return ""
}

// StartOn запускает сервер на произвольном адресе и возвращает фактический URL.
// Отдельная функция нужна для тестов (адрес с портом 0) и отладки.
func StartOn(addr string) (string, error) {
	mu.Lock()
	defer mu.Unlock()
	if started {
		return "", nil
	}
	h := server.NewMux(server.Options{Version: version, WebRoot: web.Content()})
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", fmt.Errorf("не удалось запустить сервер: %w", err)
	}
	srv := &http.Server{Handler: h}
	a := ln.Addr().String()
	activeServer = srv
	activeListener = ln
	started = true
	go func() {
		err := srv.Serve(ln)
		mu.Lock()
		if activeServer == srv {
			started = false
			activeServer = nil
			activeListener = nil
		}
		mu.Unlock()
		if err != nil && err != http.ErrServerClosed {
			log.Printf("сервер остановлен: %v", err)
		}
	}()
	url := "http://" + a + "/"
	if host, _, err := net.SplitHostPort(a); err == nil && (host == "0.0.0.0" || host == "::") {
		url = "http://" + net.JoinHostPort("127.0.0.1", portOf(a)) + "/"
	}
	log.Printf("MikroTik Inspector %s: %s", version, url)
	return url, nil
}

func portOf(addr string) string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "8765"
	}
	return port
}

// Stop останавливает HTTP-сервер и освобождает порт.
func Stop() {
	mu.Lock()
	defer mu.Unlock()
	if activeServer != nil {
		_ = activeServer.Close()
		activeServer = nil
	}
	if activeListener != nil {
		_ = activeListener.Close()
		activeListener = nil
	}
	started = false
}

// URL возвращает адрес интерфейса внутри приложения.
func URL(port int) string {
	if port <= 0 || port > 65535 {
		port = 8765
	}
	return "http://127.0.0.1:" + strconv.Itoa(port) + "/"
}

// Version возвращает версию программы.
func Version() string { return version }

// LocalURLs возвращает адреса для доступа к интерфейсу с компьютера
// в той же сети (телефон и компьютер должны быть в одной Wi-Fi сети).
func LocalURLs(port int) string {
	if port <= 0 || port > 65535 {
		port = 8765
	}
	urls := server.LocalURLs(port)
	out := ""
	for _, u := range urls {
		if out != "" {
			out += "\n"
		}
		out += u
	}
	return out
}
