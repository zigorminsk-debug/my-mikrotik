// Package server — общий HTTP-слой программы: HTML-интерфейс, API
// (/api/info, /api/interfaces, /api/config, /api/apply, /api/log), иконки и
// манифест PWA.
//
// Пакет используют и настольная версия, и Android-приложение: обе запускают
// локальный сервер и показывают одну и ту же страницу.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/zigorminsk-debug/my-mikrotik/mikrotik"
)

// Options — параметры HTTP-слоя.
type Options struct {
	// Version — версия программы (подставляется при сборке).
	Version string
	// WebRoot — файлы интерфейса (обычно web.Content()).
	WebRoot fs.FS
}

type apiResponse struct {
	OK      bool           `json:"ok"`
	Info    *mikrotik.Info `json:"info,omitempty"`
	Error   string         `json:"error,omitempty"`
	Version string         `json:"version,omitempty"`
}

// NewMux собирает маршруты HTTP-слоя.
func NewMux(opt Options) http.Handler {
	ver := opt.Version
	root := opt.WebRoot

	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.FS(root)))

	// Иконки и манифест — нужны при добавлении страницы на домашний экран
	// Android, а также для отображения в браузере.
	mux.HandleFunc("/icon.png", serveIcon)
	mux.HandleFunc("/icon-192.png", serveIcon)
	mux.HandleFunc("/icon-512.png", serveIcon)
	mux.HandleFunc("/manifest.webmanifest", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/manifest+json; charset=utf-8")
		_, _ = w.Write([]byte(ManifestJSON))
	})

	mux.HandleFunc("/api/discovery", handleDiscovery)
	mux.HandleFunc("/api/info", handleInfo(ver))
	mux.HandleFunc("/api/interfaces", handleInterfaces(ver))
	mux.HandleFunc("/api/config", handleConfig)
	mux.HandleFunc("/api/geo/countries", handleGeoCountries)
	mux.HandleFunc("/api/geo/prefixes", handleGeoPrefixes)
	mux.HandleFunc("/api/apply", handleApply(ver))
	mux.HandleFunc("/api/log", handleLog(ver))

	return withSecurityHeaders(mux)
}

// withSecurityHeaders добавляет базовые заголовки безопасности.
func withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; img-src 'self' data:")
		next.ServeHTTP(w, r)
	})
}

func serveIcon(w http.ResponseWriter, r *http.Request) {
	size := 512
	switch r.URL.Path {
	case "/icon-192.png":
		size = 192
	case "/icon.png":
		size = 256
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(IconPNG(size))
}

// ManifestJSON — манифест веб-приложения (добавление на домашний экран Android).
const ManifestJSON = `{
  "name": "MikroTik Inspector",
  "short_name": "MikroTik",
  "description": "Модель, прошивка, пакеты и конструктор конфигурации MikroTik",
  "start_url": "/",
  "scope": "/",
  "display": "standalone",
  "background_color": "#0f172a",
  "theme_color": "#0f172a",
  "lang": "ru",
  "icons": [
    { "src": "/icon-192.png", "sizes": "192x192", "type": "image/png", "purpose": "any" },
    { "src": "/icon-512.png", "sizes": "512x512", "type": "image/png", "purpose": "any maskable" }
  ]
}
`

// handleInfo принимает параметры подключения и возвращает информацию с роутера.
func handleInfo(ver string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
			return
		}
		var p mikrotik.Params
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&p); err != nil {
			writeJSON(w, http.StatusBadRequest, apiResponse{OK: false, Error: "некорректный запрос: " + err.Error()})
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()

		info, err := mikrotik.Collect(ctx, p)
		if err != nil {
			writeJSON(w, http.StatusOK, apiResponse{OK: false, Error: err.Error(), Version: ver})
			return
		}
		writeJSON(w, http.StatusOK, apiResponse{OK: true, Info: info, Version: ver})
	}
}

// handleInterfaces возвращает список интерфейсов роутера для конструктора.
func handleInterfaces(ver string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			mikrotik.Params
			Device *mikrotik.Info `json:"device"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, apiResponse{OK: false, Error: "некорректный запрос: " + err.Error()})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()

		list, err := mikrotik.CollectInterfaces(ctx, req.Params)
		if err != nil {
			writeJSON(w, http.StatusOK, apiResponse{OK: false, Error: err.Error(), Version: ver})
			return
		}
		// Доступность разделов конструктора зависит от модели, прошивки и пакетов,
		// поэтому считаем её здесь же — интерфейсу достаточно одного запроса.
		writeJSON(w, http.StatusOK, struct {
			OK         bool                   `json:"ok"`
			Interfaces []mikrotik.Iface       `json:"interfaces"`
			Features   []mikrotik.FeatureInfo `json:"features"`
			Version    string                 `json:"version,omitempty"`
		}{OK: true, Interfaces: list, Features: mikrotik.AnalyzeFeatures(req.Device, list), Version: ver})
	}
}

// handleConfig генерирует скрипт конфигурации по данным об устройстве.
func handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	var req mikrotik.ConfigRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{OK: false, Error: "некорректный запрос: " + err.Error()})
		return
	}
	if req.Device == nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{OK: false, Error: "не переданы данные об устройстве — сначала подключитесь"})
		return
	}
	res := mikrotik.Generate(req.Device, req.Interfaces, &req.Settings, time.Now())
	writeJSON(w, http.StatusOK, res)
}

// handleApply применяет сформированный скрипт на роутере.
// handleLog возвращает записи журнала роутера. Это основной способ понять, как
// отработал применённый скрипт: вывод команд наружу не передаётся, поэтому
// скрипты пишут результат в журнал.
func handleLog(ver string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			mikrotik.Params
			Filter string `json:"filter"`
			Limit  int    `json:"limit"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, apiResponse{OK: false, Error: "некорректный запрос: " + err.Error()})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()

		filter := strings.TrimSpace(req.Filter)
		if filter == "" {
			filter = "mti" // маркер сообщений наших скриптов
		}
		list, err := mikrotik.CollectLog(ctx, req.Params, filter, req.Limit)
		if err != nil {
			writeJSON(w, http.StatusOK, apiResponse{OK: false, Error: err.Error(), Version: ver})
			return
		}
		writeJSON(w, http.StatusOK, struct {
			OK      bool                `json:"ok"`
			Log     []mikrotik.LogEntry `json:"log"`
			Version string              `json:"version,omitempty"`
		}{OK: true, Log: list, Version: ver})
	}
}

func handleApply(ver string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Conn   mikrotik.Params `json:"conn"`
			Script string          `json:"script"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, apiResponse{OK: false, Error: "некорректный запрос: " + err.Error()})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()

		proto, err := mikrotik.ApplyScript(ctx, req.Conn, req.Script)
		if err != nil {
			writeJSON(w, http.StatusOK, apiResponse{OK: false, Error: err.Error(), Version: ver})
			return
		}
		writeJSON(w, http.StatusOK, struct {
			OK       bool   `json:"ok"`
			Protocol string `json:"protocol"`
			Script   string `json:"script"` // имя служебного скрипта на роутере
			Version  string `json:"version,omitempty"`
		}{OK: true, Protocol: proto, Script: mikrotik.ScriptName(), Version: ver})
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// LocalIPs возвращает IPv4-адреса машины в локальной сети — по ним страницу
// интерфейса можно открыть с телефона (полезно для Android-версии).
func LocalIPs() []string {
	var out []string
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			var ip net.IP
			switch v := a.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip4 := ip.To4(); ip4 != nil {
				out = append(out, ip4.String())
			}
		}
	}
	return out
}

// LocalURLs возвращает адреса, по которым страница доступна из локальной сети.
func LocalURLs(port int) []string {
	ips := LocalIPs()
	urls := make([]string, 0, len(ips))
	for _, ip := range ips {
		urls = append(urls, "http://"+net.JoinHostPort(ip, strconv.Itoa(port)))
	}
	return urls
}

// ListenAndServe запускает сервер на addr и возвращает фактический адрес
// (полезно при addr с портом 0). Работает до остановки сервера или ошибки.
func ListenAndServe(addr string, h http.Handler, ready func(addr string) error) error {
	return ListenAndServeUntil(addr, h, ready, nil)
}

// ListenAndServeUntil closes the server when the desktop window exits.
// A nil done channel keeps the server running until it fails.
func ListenAndServeUntil(addr string, h http.Handler, ready func(string) error, done func() <-chan error) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("не удалось запустить сервер: %w", err)
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	// The listener must serve before the GUI navigates to it. Closing the
	// listener explicitly also covers a callback failure before Serve starts.
	if ready != nil {
		if err := ready(ln.Addr().String()); err != nil {
			_ = srv.Close()
			_ = ln.Close()
			<-served
			return err
		}
	}
	var window <-chan error
	if done != nil {
		window = done()
	}
	select {
	case err := <-served:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	case windowErr := <-window:
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			_ = srv.Close()
		}
		<-served
		return windowErr
	}
}
