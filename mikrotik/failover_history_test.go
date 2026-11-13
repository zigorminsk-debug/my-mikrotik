package mikrotik

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func TestFailoverHistoryPrivacyAndPatterns(t *testing.T) {
	cases := []struct{ msg, event string }{
		{"mti-failover: backup=true, приоритет основного канала 3", "Контроллер сообщил о переключении"},
		{"mti-failover: состояние — ответов 2 из 3, backup=false, приоритет=1, активный=192.0.2.1%lte1, маршрутов=3, из них динамических 2", "Периодическое состояние контроллера"},
		{"mti-failover: нет ответа от 8.8.8.8 (0 из 3, тип=ether), отсчёт 30 c", "Нет ответа контрольной пробы; начат отсчёт"},
		{"mti-failover: ответ от 8.8.8.8 получен (2 из 3), отсчёт 60 c", "Ответ контрольной пробы; начат отсчёт"},
		{"mti-failover: переключение не удалось: приоритет остался 3 вместо 1", "Контроллер сообщил, что переключение не удалось"},
		{"mti-failover: приоритет основного канала остался 3 вместо 1, повторяю", "Контроллер сообщил о повторной попытке"},
		{"mti-failover: создан маршрут основного канала, приоритет 1", "Контроллер сообщил о создании маршрута"},
		{"mti-failover: маршрут основного канала обновлён, приоритет 3", "Контроллер сообщил об обновлении маршрута"},
		{"mti-failover: нет шлюза основного канала — проба пропущена", "Нет шлюза основного канала; проба пропущена"},
		{"mti-failover: не удалось изменить приоритет основного канала", "Не удалось изменить приоритет основного канала"},
		{"mti-failover: backup=true, приоритет основного канала 3 SECRET", ""},
		{"user login SECRET", ""},
		{"mti-failover: нет ответа от PRIVATESECRET (0 из 3, тип=ether), отсчёт 30 c", "Нет ответа контрольной пробы; начат отсчёт"},
	}
	for _, tc := range cases {
		t.Run(tc.msg, func(t *testing.T) {
			row := failoverEvent(map[string]string{"message": tc.msg, "time": "2026-11-12 11:57:00", "topics": "PRIVATE", "other": "SECRET"})
			if tc.event == "" {
				if row != nil {
					t.Fatalf("unexpected: %v", row)
				}
				return
			}
			if row["event"] != tc.event || row["time"] != "2026-11-12 11:57:00" {
				t.Fatalf("%v", row)
			}
			b, _ := json.Marshal(row)
			for _, secret := range []string{"SECRET", "PRIVATE", "192.0.2.1", "8.8.8.8", "lte1"} {
				if strings.Contains(string(b), secret) {
					t.Fatalf("leak: %s", b)
				}
			}
		})
	}
	for _, time := range []string{"11:57:00", "nov/12 11:57:00", "nov/12/2026 11:57:00", "2026-11-12 11:57:00"} {
		if failoverEvent(map[string]string{"message": cases[0].msg, "time": time})["time"] != time {
			t.Errorf("lost time %s", time)
		}
	}
	if _, ok := failoverEvent(map[string]string{"message": cases[0].msg, "time": "SECRET"})["time"]; ok {
		t.Fatal("unrecognised time must not be returned")
	}
}

func TestHistoryLimitAndOptIn(t *testing.T) {
	calls := []string{}
	read := func(path string) ([]map[string]string, error) {
		calls = append(calls, path)
		if path != "/log" {
			return nil, nil
		}
		records := []map[string]string{{"message": "not controller"}}
		for i := 0; i < 105; i++ {
			records = append(records, map[string]string{"time": fmt.Sprintf("11:57:%02d", i%60), "message": fmt.Sprintf("mti-failover: backup=true, приоритет основного канала %d", i)})
		}
		return records, nil
	}
	old := buildSnapshot(context.Background(), "rest", read, false)
	if _, ok := old.Sections["failoverHistory"]; ok || strings.Contains(strings.Join(calls, ","), "/log") {
		t.Fatal("history must be opt in")
	}
	calls = nil
	s := buildSnapshot(context.Background(), "rest", read, false, true)
	h := s.Sections["failoverHistory"]
	if h.Status != "ok" || !h.Truncated || len(h.Rows) != 100 || h.Rows[0]["distance"] != "5" || h.Rows[99]["distance"] != "104" {
		t.Fatalf("history=%+v", h)
	}
	if calls[len(calls)-1] != "/log" {
		t.Fatalf("calls=%v", calls)
	}
	failed := func(path string) ([]map[string]string, error) {
		if path == "/log" {
			return nil, fmt.Errorf("SECRET")
		}
		return nil, nil
	}
	h = buildSnapshot(context.Background(), "rest", failed, false, true).Sections["failoverHistory"]
	if h.Status != "error" || len(h.Rows) != 0 {
		t.Fatalf("%+v", h)
	}
	aborted := func(string) ([]map[string]string, error) { return nil, fmt.Errorf("fail") }
	h = buildSnapshot(context.Background(), "api", aborted, true, true).Sections["failoverHistory"]
	if h.Status != "skipped" {
		t.Fatalf("API error must skip history: %+v", h)
	}
}

func TestCollectFailoverHistoryREST(t *testing.T) {
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/rest/system/identity":
			fmt.Fprint(w, `[{"name":"router"}]`)
		case "/rest/system/resource":
			fmt.Fprint(w, `[{"version":"7.15","board-name":"hAP","architecture-name":"arm"}]`)
		case "/rest/log":
			fmt.Fprint(w, `[{"time":"11:57:00","message":"mti-failover: backup=true, приоритет основного канала 3"},{"message":"SECRET"}]`)
		default:
			fmt.Fprint(w, `[]`)
		}
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	_, ps, _ := net.SplitHostPort(u.Host)
	port, _ := strconv.Atoi(ps)
	p := Params{Host: "127.0.0.1", RESTPort: port, User: "admin", Password: "password", Protocol: "rest"}
	expected := &Info{Protocol: "rest", Identity: "router", RouterOS: "7.15", Architecture: "arm", Diagnostics: &IdentityDiagnostics{ResourceBoard: "hAP"}}
	snap, err := CollectNetworkSnapshot(context.Background(), p, expected, true)
	if err != nil {
		t.Fatal(err)
	}
	h := snap.Sections["failoverHistory"]
	if h.Status != "ok" || len(h.Rows) != 1 || h.Rows[0]["backup"] != "true" {
		t.Fatalf("%+v", h)
	}
	for _, call := range calls {
		if !strings.HasPrefix(call, "GET ") {
			t.Fatalf("unexpected write %s", call)
		}
	}
	if calls[len(calls)-1] != "GET /rest/log" {
		t.Fatalf("%v", calls)
	}
	b, _ := json.Marshal(snap)
	if strings.Contains(string(b), "SECRET") || strings.Contains(string(b), "password") {
		t.Fatalf("%s", b)
	}
	calls = nil
	expected.Identity = "other"
	if _, err = CollectNetworkSnapshot(context.Background(), p, expected, true); err == nil {
		t.Fatal("identity mismatch accepted")
	}
	for _, call := range calls {
		if strings.HasSuffix(call, "/log") {
			t.Fatal("log read before identity check")
		}
	}
}

func TestCollectFailoverHistoryAPI(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan []string, 1)
	go func() {
		conn, e := ln.Accept()
		if e != nil {
			got <- nil
			return
		}
		defer conn.Close()
		var cmds []string
		defer func() { got <- cmds }()
		for {
			s, e := readSentence(conn)
			if e != nil || len(s) == 0 {
				return
			}
			cmds = append(cmds, s[0])
			switch s[0] {
			case "/system/identity/print":
				_ = writeSentence(conn, "!re", "=name=router")
			case "/system/resource/print":
				_ = writeSentence(conn, "!re", "=version=7.15", "=board-name=hAP", "=architecture-name=arm")
			case "/log/print":
				_ = writeSentence(conn, "!re", "=time=11:57:00", "=message=mti-failover: backup=false, приоритет основного канала 1")
			}
			_ = writeSentence(conn, "!done")
		}
	}()
	p := Params{Host: "127.0.0.1", APIPort: ln.Addr().(*net.TCPAddr).Port, User: "admin", Protocol: "api"}
	expected := &Info{Protocol: "api", Identity: "router", RouterOS: "7.15", Architecture: "arm", Diagnostics: &IdentityDiagnostics{ResourceBoard: "hAP"}}
	s, err := CollectNetworkSnapshot(context.Background(), p, expected, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Sections["failoverHistory"].Rows) != 1 {
		t.Fatal(s.Sections)
	}
	commands := <-got
	for _, c := range commands {
		if c != "/login" && !strings.HasSuffix(c, "/print") {
			t.Fatalf("unexpected write: %v", commands)
		}
	}
	if commands[len(commands)-1] != "/log/print" {
		t.Fatalf("%v", commands)
	}
}
