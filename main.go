// MikroTik Inspector — настольное HTML-GUI (на Go) для Windows/Linux/macOS.
//
// Программа запускает локальный HTTP-сервер с веб-интерфейсом и открывает
// его в браузере по умолчанию. Через интерфейс можно подключиться к роутеру
// MikroTik (RouterOS API или REST API), получить модель, версию прошивки,
// список пакетов и собрать конфигурацию.
//
// Версия для Android — в командe android/ (см. docs/STEPS.md).
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"

	"github.com/zigorminsk-debug/my-mikrotik/internal/server"
	"github.com/zigorminsk-debug/my-mikrotik/web"
)

// version подставляется при релизной сборке через -ldflags "-X main.version=...".
// Версия отображается в приложении и используется для CI-артефактов.
var version = "0.2.21"

func main() {
	closeLog := initLogging()
	defer closeLog()
	addr := flag.String("addr", "127.0.0.1:0", "адрес HTTP-сервера GUI (по умолчанию — случайный локальный порт)")
	noBrowser := flag.Bool("no-browser", false, "не открывать браузер автоматически")
	showVersion := flag.Bool("version", false, "вывести версию и выйти")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	// instrument позволяет заметить окно браузера, которое открылось,
	// но не показало страницу (см. openDesktop в desktop_windows.go).
	var deviceStore *server.DeviceStore
	if configDir, err := os.UserConfigDir(); err == nil {
		deviceStore = server.NewDeviceStore(filepath.Join(configDir, "MikroTik Inspector", "devices.json"))
	}
	h := instrument(server.NewMux(server.Options{Version: version, WebRoot: web.Content(), DeviceStore: deviceStore}))

	log.Printf("MikroTik Inspector %s запущен", version)
	var done <-chan error
	var stopDesktop func()
	err := server.ListenAndServeUntil(*addr, h, func(actual string) error {
		pageURL := "http://" + actual
		log.Printf("Интерфейс: %s", pageURL)
		// Если сервер слушает не только локальный адрес, подскажем адреса
		// для открытия страницы с телефона.
		if host, port, err := net.SplitHostPort(actual); err == nil && (host == "0.0.0.0" || host == "::") {
			if n, perr := strconv.Atoi(port); perr == nil {
				for _, u := range server.LocalURLs(n) {
					log.Printf("Доступ в локальной сети: %s", u)
				}
			}
		}
		if !*noBrowser {
			var err error
			done, stopDesktop, err = openDesktop(pageURL)
			if err != nil {
				return err
			}
		}
		return nil
	}, func() <-chan error { return done })
	if stopDesktop != nil {
		stopDesktop()
	}
	if err != nil {
		showStartupError(err)
		log.Printf("%v", err)
	}
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
