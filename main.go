// MikroTik Inspector — настольное HTML-GUI (на Go) для Windows/Linux/macOS.
//
// Программа запускает локальный HTTP-сервер с веб-интерфейсом и открывает
// его в браузере по умолчанию. Через интерфейс можно подключиться к роутеру
// MikroTik (RouterOS API или REST API) и получить модель, версию прошивки
// и список установленных пакетов.
package main

import (
	"context"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"time"

	"github.com/zigorminsk-debug/my-mikrotik/mikrotik"
)

//go:embed web
var webContent embed.FS

// version подставляется при сборке через -ldflags "-X main.version=...".
var version = "dev"

type apiResponse struct {
	OK      bool           `json:"ok"`
	Info    *mikrotik.Info `json:"info,omitempty"`
	Error   string         `json:"error,omitempty"`
	Version string         `json:"version,omitempty"`
}

func main() {
	addr := flag.String("addr", "127.0.0.1:0", "адрес HTTP-сервера GUI (по умолчанию — случайный локальный порт)")
	noBrowser := flag.Bool("no-browser", false, "не открывать браузер автоматически")
	showVersion := flag.Bool("version", false, "вывести версию и выйти")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	web, err := fs.Sub(webContent, "web")
	if err != nil {
		log.Fatalf("встроенные файлы интерфейса повреждены: %v", err)
	}

	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.FS(web)))
	mux.HandleFunc("/api/info", handleInfo)

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("не удалось запустить локальный сервер: %v", err)
	}
	pageURL := "http://" + ln.Addr().String()

	log.Printf("MikroTik Inspector %s запущен", version)
	log.Printf("Интерфейс: %s", pageURL)
	if !*noBrowser {
		openBrowser(pageURL)
	}

	srv := &http.Server{
		Handler:           withSecurityHeaders(mux),
		ReadHeaderTimeout: 10 * time.Second,
	}
	if err := srv.Serve(ln); err != nil {
		log.Fatalf("сервер остановлен: %v", err)
	}
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

// handleInfo принимает параметры подключения и возвращает информацию с роутера.
func handleInfo(w http.ResponseWriter, r *http.Request) {
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
		writeJSON(w, http.StatusOK, apiResponse{OK: false, Error: err.Error(), Version: version})
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{OK: true, Info: info, Version: version})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// openBrowser открывает страницу GUI в браузере по умолчанию.
func openBrowser(pageURL string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", pageURL)
	case "darwin":
		cmd = exec.Command("open", pageURL)
	default:
		cmd = exec.Command("xdg-open", pageURL)
	}
	if err := cmd.Start(); err != nil {
		log.Printf("не удалось открыть браузер автоматически; откройте вручную: %s", pageURL)
	}
}
