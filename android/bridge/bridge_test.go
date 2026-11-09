package bridge

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Тест проверяет, что встроенный сервер поднимается и отдаёт интерфейс —
// ровно то, что делает Android-обвязка при запуске приложения.
func TestStartOnServesInterface(t *testing.T) {
	t.Cleanup(Stop)
	url, err := StartOn("127.0.0.1:0")
	if err != nil {
		t.Fatalf("StartOn: %v", err)
	}
	if !strings.HasPrefix(url, "http://127.0.0.1:") {
		t.Fatalf("адрес = %q", url)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("статус %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "MikroTik Inspector") {
		t.Error("в странице нет заголовка приложения")
	}

	// Конструктор доступен через API того же сервера.
	resp2, err := client.Post(url+"api/config", "application/json", strings.NewReader(`{
	  "device": {"model":"hAP ac2","routeros":"7.15.3","packages":[{"name":"routeros","version":"7.15.3"}]},
	  "interfaces": [{"name":"ether1","type":"ethernet"},{"name":"wlan1","type":"wireless"}],
	  "settings": {"wifi":{"enabled":true,"iface":"wlan1","ssid":"Test","passphrase":"passw0rd1","country":"BY"}}
	}`))
	if err != nil {
		t.Fatalf("POST config: %v", err)
	}
	defer func() { _ = resp2.Body.Close() }()
	data, _ := io.ReadAll(resp2.Body)
	if !strings.Contains(string(data), `"ok":true`) {
		t.Errorf("ответ config: %s", string(data)[:min(len(data), 300)])
	}
	if !strings.Contains(string(data), "wireless set wlan1") {
		t.Errorf("в скрипте нет настроек Wi-Fi: %s", string(data)[:min(len(data), 300)])
	}
}

func TestURLAndVersion(t *testing.T) {
	if got := URL(8765); got != "http://127.0.0.1:8765/" {
		t.Errorf("URL = %q", got)
	}
	if got := URL(0); got != "http://127.0.0.1:8765/" {
		t.Errorf("URL(0) = %q", got)
	}
	if Version() == "" {
		t.Error("пустая версия")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestStopClosesAndRestarts(t *testing.T) {
	url, err := StartOn("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	Stop()
	client := &http.Client{Timeout: time.Second}
	if resp, err := client.Get(url); err == nil {
		resp.Body.Close()
		t.Fatal("server still listening")
	}
	if _, err := StartOn("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	Stop()
}
