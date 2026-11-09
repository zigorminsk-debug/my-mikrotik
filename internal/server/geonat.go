package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zigorminsk-debug/my-mikrotik/mikrotik"
)

// Country codes present in the ipverse RIR dataset, retrieved 2026-11-09.
const geoCountryCodes = `ad ae af ag ai al am ao aq ar as at au aw ax az ba bb bd be bf bg bh bi bj bl bm bn bo bq br bs bt bw by bz ca cd cf cg ch ci ck cl cm cn co cr cu cv cw cy cz de dj dk dm do dz ec ee eg er es et fi fj fk fm fo fr ga gb gd ge gf gg gh gi gl gm gn gp gq gr gt gu gw gy hk hn hr ht hu id ie il im in io iq ir is it je jm jo jp ke kg kh ki km kn kp kr kw ky kz la lb lc li lk lr ls lt lu lv ly ma mc md me mf mg mh mk ml mm mn mo mp mq mr ms mt mu mv mw mx my mz na nc ne nf ng ni nl no np nr nu nz om pa pe pf pg ph pk pl pm pr ps pt pw py qa re ro rs ru rw sa sb sc sd se sg si sk sl sm sn so sr ss st sv sx sy sz tc td tg th tj tk tl tm tn to tr tt tv tw tz ua ug us uy uz va vc ve vg vi vn vu wf ws ye yt za zm zw`

var geoCache = struct {
	sync.Mutex
	data map[string]geoCacheEntry
}{data: map[string]geoCacheEntry{}}

type geoCacheEntry struct {
	prefixes []string
	fetched  time.Time
}

var geoHTTPClient = &http.Client{Timeout: 30 * time.Second}

type geoResponse struct {
	OK        bool     `json:"ok"`
	Error     string   `json:"error,omitempty"`
	Countries []string `json:"countries,omitempty"`
	Prefixes  []string `json:"prefixes,omitempty"`
	FetchedAt string   `json:"fetchedAt,omitempty"`
}

func geoAllowed(code string) bool {
	for _, c := range strings.Fields(geoCountryCodes) {
		if c == code {
			return true
		}
	}
	return false
}
func handleGeoCountries(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, geoResponse{OK: true, Countries: strings.Fields(geoCountryCodes)})
}
func handleGeoPrefixes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Countries []string `json:"countries"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 16384)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, geoResponse{Error: "некорректный запрос"})
		return
	}
	if len(req.Countries) == 0 || len(req.Countries) > 12 {
		writeJSON(w, http.StatusBadRequest, geoResponse{Error: "выберите от 1 до 12 стран"})
		return
	}
	seen := map[string]bool{}
	result := []string{}
	oldest := time.Now()
	for _, code := range req.Countries {
		if !geoAllowed(code) || seen[code] {
			writeJSON(w, http.StatusBadRequest, geoResponse{Error: "неизвестная или повторная страна: " + code})
			return
		}
		seen[code] = true
		prefixes, fetched, err := loadCountry(r.Context(), geoHTTPClient, code)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, geoResponse{Error: fmt.Sprintf("не удалось загрузить %s: %v", strings.ToUpper(code), err)})
			return
		}
		if fetched.Before(oldest) {
			oldest = fetched
		}
		result = append(result, prefixes...)
		if len(result) > mikrotik.MaxGeoPrefixes {
			writeJSON(w, http.StatusBadRequest, geoResponse{Error: "слишком много подсетей: выберите меньше стран"})
			return
		}
	}
	unique := make(map[string]bool, len(result))
	all := make([]string, 0, len(result))
	for _, p := range result {
		if !unique[p] {
			unique[p] = true
			all = append(all, p)
		}
	}
	sort.Strings(all)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, geoResponse{OK: true, Countries: req.Countries, Prefixes: all, FetchedAt: oldest.UTC().Format(time.RFC3339)})
}
func loadCountry(ctx context.Context, client *http.Client, code string) ([]string, time.Time, error) {
	if !geoAllowed(code) {
		return nil, time.Time{}, fmt.Errorf("неизвестный код страны")
	}
	geoCache.Lock()
	cached, ok := geoCache.data[code]
	geoCache.Unlock()
	if ok && time.Since(cached.fetched) < 12*time.Hour {
		return cached.prefixes, cached.fetched, nil
	}
	url := "https://api.github.com/repos/ipverse/country-ip-blocks/contents/country/" + code + "/ipv4-aggregated.txt"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, time.Time{}, err
	}
	req.Header.Set("User-Agent", "MikroTik-Inspector")
	req.Header.Set("Accept", "application/vnd.github+json")
	res, err := client.Do(req)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, time.Time{}, fmt.Errorf("источник вернул HTTP %d", res.StatusCode)
	}
	var file struct {
		Encoding string `json:"encoding"`
		Content  string `json:"content"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(&file); err != nil {
		return nil, time.Time{}, fmt.Errorf("некорректный ответ источника")
	}
	if file.Encoding != "base64" {
		return nil, time.Time{}, fmt.Errorf("источник не предоставил данные в ожидаемом формате")
	}
	decoded, err := base64.StdEncoding.DecodeString(file.Content)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("некорректные данные источника")
	}
	prefixes := []string{}
	for _, line := range strings.Split(string(decoded), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		ip, block, err := net.ParseCIDR(line)
		if err != nil || ip.To4() == nil {
			return nil, time.Time{}, fmt.Errorf("источник содержит неверную IPv4-подсеть")
		}
		ones, bits := block.Mask.Size()
		if bits != 32 {
			return nil, time.Time{}, fmt.Errorf("источник содержит не IPv4-подсеть")
		}
		if ones == 0 {
			return nil, time.Time{}, fmt.Errorf("источник содержит 0.0.0.0/0")
		}
		prefixes = append(prefixes, block.String())
		if len(prefixes) > mikrotik.MaxGeoPrefixes {
			return nil, time.Time{}, fmt.Errorf("слишком много подсетей")
		}
	}
	if len(prefixes) == 0 {
		return nil, time.Time{}, fmt.Errorf("источник вернул пустой список")
	}
	fetched := time.Now()
	geoCache.Lock()
	geoCache.data[code] = geoCacheEntry{prefixes: prefixes, fetched: fetched}
	geoCache.Unlock()
	return prefixes, fetched, nil
}
