package mikrotik

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	mockUser     = "admin"
	mockPassword = "secret"
	mockIdentity = "TEST-ROUTER"
	// Эталон: "00" + md5(0x00 || "secret" || challenge).
	mockChallenge = "00112233445566778899aabbccddeeff"
)

// --- кодек протокола ---

func TestEncodeLength(t *testing.T) {
	cases := []struct {
		in   int
		want []byte
	}{
		{0, []byte{0x00}},
		{1, []byte{0x01}},
		{0x7F, []byte{0x7F}},
		{0x80, []byte{0x80, 0x80}},
		{0x3FFF, []byte{0xBF, 0xFF}},
		{0x4000, []byte{0xC0, 0x40, 0x00}},
		{0x1FFFFF, []byte{0xDF, 0xFF, 0xFF}},
		{0x200000, []byte{0xE0, 0x20, 0x00, 0x00}},
		{0x0FFFFFFF, []byte{0xEF, 0xFF, 0xFF, 0xFF}},
	}
	for _, c := range cases {
		got, err := encodeLength(c.in)
		if err != nil {
			t.Fatalf("encodeLength(%d): %v", c.in, err)
		}
		if !bytes.Equal(got, c.want) {
			t.Errorf("encodeLength(%d) = %x, ожидалось %x", c.in, got, c.want)
		}
	}
	if _, err := encodeLength(0x10000000); err == nil {
		t.Error("encodeLength(0x10000000): ожидалась ошибка")
	}
}

func TestSentenceRoundTrip(t *testing.T) {
	words := []string{
		"/system/resource/print",
		"=board-name=hEX S",
		"=" + strings.Repeat("k", 127) + "=v",
		strings.Repeat("b", 128),
		strings.Repeat("c", 0x3FFF),
		strings.Repeat("d", 0x4000),
	}
	var buf bytes.Buffer
	if err := writeSentence(&buf, words...); err != nil {
		t.Fatalf("writeSentence: %v", err)
	}
	got, err := readSentence(&buf)
	if err != nil {
		t.Fatalf("readSentence: %v", err)
	}
	if len(got) != len(words) {
		t.Fatalf("прочитано слов: %d, ожидалось %d", len(got), len(words))
	}
	for i := range words {
		if got[i] != words[i] {
			t.Errorf("слово %d: получено %q…, ожидалось %q…", i, truncate(got[i]), truncate(words[i]))
		}
	}
	if buf.Len() != 0 {
		t.Errorf("лишние байты после предложения: %d", buf.Len())
	}
}

func truncate(s string) string {
	if len(s) > 30 {
		return s[:30]
	}
	return s
}

func TestReadSentenceRejectsOversizedAndReservedLengths(t *testing.T) {
	for _, in := range [][]byte{
		{0xf0, 0xff, 0xff, 0xff, 0xff}, // cannot fit a 32-bit int and must not allocate
		{0xf1, 0, 0, 0, 1},             // reserved control prefix
	} {
		if _, err := readSentence(bytes.NewReader(in)); err == nil {
			t.Errorf("accepted invalid length %x", in)
		}
	}
	prefix, _ := encodeLength(maxAPIWordBytes + 1)
	if _, err := readSentence(bytes.NewReader(prefix)); err == nil {
		t.Error("accepted oversized word")
	}
}

func TestReadSentenceLimitsWordCount(t *testing.T) {
	in := bytes.Repeat([]byte{1, 'x'}, maxAPISentenceWords+1)
	in = append(in, 0)
	if _, err := readSentence(bytes.NewReader(in)); err == nil {
		t.Error("accepted excessive word count")
	}
}

func TestSplitAttr(t *testing.T) {
	k, v, ok := splitAttr("=board-name=hEX S")
	if !ok || k != "board-name" || v != "hEX S" {
		t.Errorf("splitAttr: got (%q,%q,%v)", k, v, ok)
	}
	k, v, ok = splitAttr("=empty=")
	if !ok || k != "empty" || v != "" {
		t.Errorf("splitAttr пустое значение: got (%q,%q,%v)", k, v, ok)
	}
	if _, _, ok := splitAttr("!done"); ok {
		t.Error("splitAttr: слово без '=' должно быть отклонено")
	}
}

// --- авторизация ---

func TestChallengeResponse(t *testing.T) {
	got, err := challengeResponse(mockChallenge, mockPassword)
	if err != nil {
		t.Fatalf("challengeResponse: %v", err)
	}
	const want = "00c17511d224a5e93170632807d36388aa"
	if got != want {
		t.Errorf("challengeResponse = %s, ожидалось %s", got, want)
	}
	if _, err := challengeResponse("не-hex", mockPassword); err == nil {
		t.Error("ожидалась ошибка для некорректного challenge")
	}
}

// --- мок RouterOS API ---

// startMockAPI поднимает упрощённый сервер протокола RouterOS API.
// При challenge=true имитирует RouterOS < 6.43 (двухэтапный вход).
func startMockAPI(t *testing.T, challenge bool) (port int, stop func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
		for {
			s, err := readSentence(conn)
			if err != nil || len(s) == 0 {
				return
			}
			switch s[0] {
			case "/login":
				a := attrs(s[1:])
				if challenge {
					if resp, ok := a["response"]; ok {
						expected, _ := challengeResponse(mockChallenge, mockPassword)
						if resp != expected {
							_ = writeSentence(conn, "!trap", "=message=invalid user name or password")
							_ = writeSentence(conn, "!done")
							continue
						}
						_ = writeSentence(conn, "!done")
						continue
					}
					// Первый этап: имя и пароль игнорируются, шлётся challenge.
					_ = writeSentence(conn, "!done", "=ret="+mockChallenge)
					continue
				}
				if a["name"] == mockUser && a["password"] == mockPassword {
					_ = writeSentence(conn, "!done")
				} else {
					_ = writeSentence(conn, "!trap", "=message=invalid user name or password")
					_ = writeSentence(conn, "!done")
				}
			case "/system/identity/print":
				_ = writeSentence(conn, "!re", "=name="+mockIdentity)
				_ = writeSentence(conn, "!done")
			case "/system/resource/print":
				_ = writeSentence(conn, "!re",
					"=board-name=hEX S",
					"=architecture-name=arm",
					"=version=7.15.3 (stable)",
					"=uptime=1w2d3h4m5s",
					"=cpu=MT7621A")
				_ = writeSentence(conn, "!done")
			case "/system/routerboard/print":
				_ = writeSentence(conn, "!re",
					"=model=RB750Gr3",
					"=current-firmware=7.15.3",
					"=factory-firmware=7.15.3",
					"=upgrade-firmware=7.16")
				_ = writeSentence(conn, "!done")
			case "/system/package/print":
				for _, p := range [][2]string{
					{"routing", "7.15.3"},
					{"system", "7.15.3"},
					{"wifiwave2", "7.15.3"},
				} {
					_ = writeSentence(conn, "!re", "=name="+p[0], "=version="+p[1], "=status=installed")
				}
				_ = writeSentence(conn, "!done")
			default:
				_ = writeSentence(conn, "!trap", "=message=expected end of command, got "+s[0])
				_ = writeSentence(conn, "!done")
			}
		}
	}()
	stop = func() {
		_ = ln.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
		}
	}
	return ln.Addr().(*net.TCPAddr).Port, stop
}

func TestCollectViaAPI(t *testing.T) {
	port, stop := startMockAPI(t, false)
	defer stop()

	info, err := Collect(context.Background(), Params{
		Host:     "127.0.0.1",
		APIPort:  port,
		User:     mockUser,
		Password: mockPassword,
		Protocol: "api",
	})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	checkMockInfo(t, info)
	if info.Protocol != "api" {
		t.Errorf("protocol = %q, ожидалось api", info.Protocol)
	}
}

func TestCollectViaAPIChallengeLogin(t *testing.T) {
	port, stop := startMockAPI(t, true)
	defer stop()

	info, err := Collect(context.Background(), Params{
		Host:     "127.0.0.1",
		APIPort:  port,
		User:     mockUser,
		Password: mockPassword,
		Protocol: "api",
	})
	if err != nil {
		t.Fatalf("Collect (challenge login): %v", err)
	}
	checkMockInfo(t, info)
}

func TestCollectAPIWrongPassword(t *testing.T) {
	port, stop := startMockAPI(t, false)
	defer stop()

	_, err := Collect(context.Background(), Params{
		Host:     "127.0.0.1",
		APIPort:  port,
		User:     mockUser,
		Password: "wrong",
		Protocol: "api",
	})
	if err == nil {
		t.Fatal("ожидалась ошибка авторизации")
	}
	if !strings.Contains(err.Error(), "invalid user name or password") {
		t.Errorf("сообщение об ошибке: %v", err)
	}
}

func checkMockInfo(t *testing.T, info *Info) {
	t.Helper()
	if info.Identity != mockIdentity {
		t.Errorf("identity = %q, ожидалось %q", info.Identity, mockIdentity)
	}
	if info.Model != "hEX S" {
		t.Errorf("model = %q, ожидалось %q", info.Model, "hEX S")
	}
	if info.RouterOS != "7.15.3 (stable)" {
		t.Errorf("routeros = %q", info.RouterOS)
	}
	if info.CurrentFirmware != "7.15.3" {
		t.Errorf("currentFirmware = %q", info.CurrentFirmware)
	}
	if info.FactoryFirmware != "7.15.3" {
		t.Errorf("factoryFirmware = %q", info.FactoryFirmware)
	}
	if info.Board != "RB750Gr3" {
		t.Errorf("board = %q", info.Board)
	}
	if info.Architecture != "arm" || info.CPU != "MT7621A" {
		t.Errorf("architecture/cpu = %q/%q", info.Architecture, info.CPU)
	}
	if len(info.Packages) != 3 {
		t.Fatalf("пакетов: %d, ожидалось 3", len(info.Packages))
	}
	names := map[string]string{}
	for _, p := range info.Packages {
		names[p.Name] = p.Version
	}
	if names["routing"] != "7.15.3" || names["system"] != "7.15.3" || names["wifiwave2"] != "7.15.3" {
		t.Errorf("пакеты: %v", names)
	}
}

// --- мок REST API ---

func startMockREST(t *testing.T) (port int, stop func()) {
	t.Helper()
	auth := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			u, p, ok := r.BasicAuth()
			if !ok || u != mockUser || p != mockPassword {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = io.WriteString(w, `{"message":"HTTP 401 Unauthorized"}`)
				return
			}
			h(w, r)
		}
	}
	write := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/rest/system/identity", auth(func(w http.ResponseWriter, r *http.Request) {
		write(w, []map[string]any{{"name": mockIdentity}})
	}))
	mux.HandleFunc("/rest/system/resource", auth(func(w http.ResponseWriter, r *http.Request) {
		write(w, []map[string]any{{
			"board-name":        "hEX S",
			"architecture-name": "arm",
			"version":           "7.15.3 (stable)",
			"uptime":            "1w2d3h4m5s",
			"cpu":               "MT7621A",
		}})
	}))
	mux.HandleFunc("/rest/system/routerboard", auth(func(w http.ResponseWriter, r *http.Request) {
		write(w, []map[string]any{{
			"model":            "RB750Gr3",
			"current-firmware": "7.15.3",
			"factory-firmware": "7.15.3",
			"upgrade-firmware": "7.16",
		}})
	}))
	mux.HandleFunc("/rest/system/package", auth(func(w http.ResponseWriter, r *http.Request) {
		write(w, []map[string]any{
			{"name": "routing", "version": "7.15.3", "status": "installed"},
			{"name": "system", "version": "7.15.3", "status": "installed"},
			{"name": "wifiwave2", "version": "7.15.3", "status": "installed"},
		})
	}))
	srv := httptest.NewServer(mux)
	u, _ := url.Parse(srv.URL)
	p, _ := strconv.Atoi(strings.Split(u.Host, ":")[1])
	return p, srv.Close
}

func TestCollectViaREST(t *testing.T) {
	port, stop := startMockREST(t)
	defer stop()

	info, err := Collect(context.Background(), Params{
		Host:     "127.0.0.1",
		RESTPort: port,
		User:     mockUser,
		Password: mockPassword,
		Protocol: "rest",
	})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	checkMockInfo(t, info)
	if info.Protocol != "rest" {
		t.Errorf("protocol = %q, ожидалось rest", info.Protocol)
	}
}

func TestCollectRESTWrongPassword(t *testing.T) {
	port, stop := startMockREST(t)
	defer stop()

	_, err := Collect(context.Background(), Params{
		Host:     "127.0.0.1",
		RESTPort: port,
		User:     mockUser,
		Password: "wrong",
		Protocol: "rest",
	})
	if err == nil {
		t.Fatal("ожидалась ошибка авторизации")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("сообщение об ошибке: %v", err)
	}
}

// --- auto-режим и нормализация ---

func TestAutoProtocolFallsBackToREST(t *testing.T) {
	restPort_, stop := startMockREST(t)
	defer stop()

	// Порт, на котором никто не слушает, — имитация заблокированного api.
	dead, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	deadPort := dead.Addr().(*net.TCPAddr).Port
	_ = dead.Close()

	info, err := Collect(context.Background(), Params{
		Host:     "127.0.0.1",
		APIPort:  deadPort,
		RESTPort: restPort_,
		User:     mockUser,
		Password: mockPassword,
		Protocol: "auto",
	})
	if err != nil {
		t.Fatalf("Collect (auto): %v", err)
	}
	if info.Protocol != "rest" {
		t.Errorf("protocol = %q, ожидалось rest (fallback с api)", info.Protocol)
	}
	checkMockInfo(t, info)
}

func TestNormalizeParams(t *testing.T) {
	cases := []struct {
		name     string
		in       Params
		wantHost string
		wantPort int
		wantErr  bool
	}{
		{"URL с портом и путём", Params{Host: "http://192.168.88.1:8728/rest", User: "admin"}, "192.168.88.1", 8728, false},
		{"host:port в поле адреса", Params{Host: "192.168.88.1:8729", User: "admin"}, "192.168.88.1", 8729, false},
		{"поле порта важнее", Params{Host: "192.168.88.1:8728", Port: 9999, User: "admin"}, "192.168.88.1", 9999, false},
		{"простой адрес", Params{Host: " 192.168.88.1 ", User: "admin"}, "192.168.88.1", 0, false},
		{"IPv6 без порта", Params{Host: "fe80::1", User: "admin"}, "fe80::1", 0, false},
		{"IPv6 в скобках", Params{Host: "[fe80::1]", User: "admin"}, "fe80::1", 0, false},
		{"пустой адрес", Params{Host: "", User: "admin"}, "", 0, true},
		{"пустой логин", Params{Host: "192.168.88.1"}, "", 0, true},
		{"плохой протокол", Params{Host: "192.168.88.1", User: "a", Protocol: "ftp"}, "", 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := c.in
			err := p.normalize()
			if c.wantErr {
				if err == nil {
					t.Fatal("ожидалась ошибка")
				}
				return
			}
			if err != nil {
				t.Fatalf("normalize: %v", err)
			}
			if p.Host != c.wantHost {
				t.Errorf("host = %q, ожидалось %q", p.Host, c.wantHost)
			}
			if p.Port != c.wantPort {
				t.Errorf("port = %d, ожидалось %d", p.Port, c.wantPort)
			}
			if p.Protocol == "" {
				t.Error("protocol должен стать auto")
			}
		})
	}
}

func TestCollectUnreachable(t *testing.T) {
	dead, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	deadPort := dead.Addr().(*net.TCPAddr).Port
	_ = dead.Close()

	_, err = Collect(context.Background(), Params{
		Host:     "127.0.0.1",
		APIPort:  deadPort,
		User:     "admin",
		Password: "x",
		Protocol: "api",
	})
	if err == nil {
		t.Fatal("ожидалась ошибка подключения")
	}
	if !strings.Contains(err.Error(), "api:") {
		t.Errorf("ошибка должна содержать имя протокола: %v", err)
	}
}

func TestDefaultPorts(t *testing.T) {
	if p := apiPort(&Params{}); p != DefaultAPIPort {
		t.Errorf("apiPort = %d, ожидалось %d", p, DefaultAPIPort)
	}
	if p := apiPort(&Params{TLS: true}); p != DefaultAPIPortTLS {
		t.Errorf("apiPort(tls) = %d, ожидалось %d", p, DefaultAPIPortTLS)
	}
	if p := restPort(&Params{}); p != DefaultRESTPort {
		t.Errorf("restPort = %d, ожидалось %d", p, DefaultRESTPort)
	}
	if p := restPort(&Params{TLS: true}); p != DefaultRESTPortT {
		t.Errorf("restPort(tls) = %d, ожидалось %d", p, DefaultRESTPortT)
	}
	if p := apiPort(&Params{Port: 1234}); p != 1234 {
		t.Errorf("apiPort(общий порт) = %d, ожидалось 1234", p)
	}
}

type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) { return 0, nil }
func TestAPIShortWrites(t *testing.T) {
	if err := writeWord(shortWriter{}, "test"); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("word: %v", err)
	}
	if err := writeSentence(shortWriter{}); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("sentence: %v", err)
	}
}

func TestAPIResponseSentenceLimit(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer server.Close()
		if _, err := readSentence(server); err != nil {
			return
		}
		for i := 0; i < 50001; i++ {
			if err := writeSentence(server, "!re", "=name=x"); err != nil {
				return
			}
		}
	}()
	c := &apiClient{conn: client, ctx: context.Background()}
	_, _, err := c.exec("/test")
	if err == nil || !strings.Contains(err.Error(), "превышает") {
		t.Fatalf("expected response limit: %v", err)
	}
	client.Close()
	<-done
}
