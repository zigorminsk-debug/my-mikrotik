// Package web содержит встроенный HTML-интерфейс программы.
//
// Интерфейс один и тот же для настольной версии и для Android: это обычная
// HTML-страница, которую отдаёт локальный HTTP-сервер внутри процесса.
package web

import (
	"embed"
	"io/fs"
)

//go:embed index.html
var content embed.FS

// Content возвращает файловую систему с файлами интерфейса.
func Content() fs.FS { return content }
