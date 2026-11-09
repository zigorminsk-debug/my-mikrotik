//go:build tools

// Инструментальные зависимости модуля.
//
// Файл не собирается в обычную сборку (тег tools), но нужен, чтобы
// `go mod tidy` не удалял golang.org/x/mobile из графа зависимостей:
// `gomobile bind` отказывается работать, если этого модуля нет.
package android

import (
	_ "golang.org/x/mobile/bind"
)
