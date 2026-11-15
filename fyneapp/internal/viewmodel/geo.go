package viewmodel

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/zigorminsk-debug/my-mikrotik/mikrotik"
)

// CountryCodes normalizes user input but country availability is verified by
// the shared prefix provider. Country edits must invalidate downloaded data.
func CountryCodes(text string) ([]string, error) {
	fields := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return r == ',' || r == ';' || unicode.IsSpace(r) })
	if len(fields) > 12 {
		return nil, fmt.Errorf("можно выбрать до 12 стран")
	}
	seen := map[string]bool{}
	for _, code := range fields {
		if len(code) != 2 || code[0] < 'a' || code[0] > 'z' || code[1] < 'a' || code[1] > 'z' || seen[code] {
			return nil, fmt.Errorf("укажите разные двухбуквенные коды стран, например by")
		}
		seen[code] = true
	}
	sort.Strings(fields)
	return fields, nil
}

// GeoPlan retains the exact full generated script and detached input data;
// the displayed preview is not necessarily the full script.
type GeoPlan struct {
	Request mikrotik.GeoApplyRequest
	Display string
}

func BuildGeoPlan(device *mikrotik.Info, ifaces []mikrotik.Iface, geo mikrotik.GeoNATSettings, at time.Time) (*GeoPlan, error) {
	if device == nil || at.IsZero() {
		return nil, fmt.Errorf("нет данных устройства или времени предпросмотра")
	}
	d := *device
	d.Packages = append([]mikrotik.Package(nil), device.Packages...)
	if device.Diagnostics != nil {
		diag := *device.Diagnostics
		d.Diagnostics = &diag
	}
	ifaces = append([]mikrotik.Iface(nil), ifaces...)
	geo.Countries = append([]string(nil), geo.Countries...)
	geo.CountryPrefixes = append([]string(nil), geo.CountryPrefixes...)
	result := mikrotik.Generate(&d, ifaces, &mikrotik.Settings{GeoNAT: geo}, at)
	if !result.OK {
		return nil, fmt.Errorf("проверьте настройки: %s", result.Error)
	}
	if !strings.Contains(result.Script, "# MTI-GEONAT-GUARDED-APPLY") {
		return nil, fmt.Errorf("GeoNAT недоступен для выбранного устройства или пакета; скрипт не создан")
	}
	r := []rune(result.Script)
	const limit = 16000
	display := result.Script
	if len(r) > limit {
		display = string(r[:limit]) + "\n\n# Предпросмотр сокращён; это не полный исполняемый файл."
	}
	return &GeoPlan{Request: mikrotik.GeoApplyRequest{Device: &d, Interfaces: ifaces, Geo: geo, GeneratedAt: result.GeneratedAt, Script: result.Script}, Display: display}, nil
}

// GeoPreview returns only text for display.
func GeoPreview(device *mikrotik.Info, ifaces []mikrotik.Iface, geo mikrotik.GeoNATSettings, at time.Time) (string, error) {
	plan, err := BuildGeoPlan(device, ifaces, geo, at)
	if err != nil {
		return "", err
	}
	return plan.Display, nil
}

// GeoApplyOutcome treats an unclassified failure or lost response conservatively.
func GeoApplyOutcome(err error, contextErr error) string {
	if err != nil {
		var preflight *mikrotik.GeoPreflightError
		if errors.As(err, &preflight) {
			return "Остановлено до записи; настройки этим запросом не изменены: " + err.Error()
		}
		return "Ошибка после возможного начала записи. Настройки могли измениться частично; не повторяйте без проверки: " + err.Error()
	}
	if contextErr != nil {
		return "Время ожидания истекло: настройки могли измениться частично; не повторяйте без проверки."
	}
	return "Роутер ответил без ошибки. Это не подтверждает защиту: проверьте порядок правил и новые подключения. Старые FastTrack-соединения могут сохраняться."
}
