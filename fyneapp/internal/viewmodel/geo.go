package viewmodel

import (
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

// GeoPreview produces a display-only preview which may be truncated.
func GeoPreview(device *mikrotik.Info, ifaces []mikrotik.Iface, geo mikrotik.GeoNATSettings, at time.Time) (string, error) {
	result := mikrotik.Generate(device, ifaces, &mikrotik.Settings{GeoNAT: geo}, at)
	if !result.OK {
		return "", fmt.Errorf("проверьте настройки: %s", result.Error)
	}
	if !strings.Contains(result.Script, "# MTI-GEONAT-GUARDED-APPLY") {
		return "", fmt.Errorf("GeoNAT недоступен для выбранного устройства или пакета; скрипт не создан")
	}
	r := []rune(result.Script)
	const limit = 16000
	if len(r) > limit {
		return string(r[:limit]) + "\n\n# Предпросмотр сокращён; это не полный исполняемый файл.", nil
	}
	return result.Script, nil
}
