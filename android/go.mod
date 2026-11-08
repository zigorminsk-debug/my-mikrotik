module github.com/zigorminsk-debug/my-mikrotik/android

go 1.27

require (
	// Нужен для gomobile bind: инструмент проверяет, что golang.org/x/mobile
	// присутствует в графе зависимостей модуля (используется только при сборке APK).
	github.com/zigorminsk-debug/my-mikrotik v0.0.0
	golang.org/x/mobile v0.0.0-20250813145541-9a2d1a9c3d2d
)

replace github.com/zigorminsk-debug/my-mikrotik => ../
