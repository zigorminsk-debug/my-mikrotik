module github.com/zigorminsk-debug/my-mikrotik/android

go 1.27

require github.com/zigorminsk-debug/my-mikrotik v0.0.0

replace github.com/zigorminsk-debug/my-mikrotik => ../

// Зависимость golang.org/x/mobile нужна только для сборки APK (gomobile bind
// требует её в графе зависимостей). Она добавляется командой
//   go get golang.org/x/mobile@latest && go mod tidy
// в workflow .github/workflows/android.yml; в репозитории версия не фиксируется,
// чтобы не расходиться с версией установленного инструмента gomobile.
