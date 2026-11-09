//go:build !windows

package main

func openDesktop(url string) (<-chan error, func(), error) {
	openBrowser(url)
	return nil, nil, nil
}

func showStartupError(err error) {}

func initLogging() func() { return func() {} }
