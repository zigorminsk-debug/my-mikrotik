//go:build !windows

package main

import "net/http"

func openDesktop(url string) (<-chan error, func(), error) {
	openBrowser(url)
	return nil, nil, nil
}

// instrument на Windows считает обращения к интерфейсу, чтобы заметить
// пустое (белое) окно браузера. На остальных системах обёртка не нужна.
func instrument(h http.Handler) http.Handler { return h }

func showStartupError(err error) {}

func initLogging() func() { return func() {} }
