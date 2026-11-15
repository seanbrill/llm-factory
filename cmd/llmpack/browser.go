package main

import (
	"errors"
	"os/exec"
	"runtime"
)

// openBrowser opens a URL in the default browser.
func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		p, err := exec.LookPath("xdg-open")
		if err != nil {
			return errors.New("no xdg-open on this computer")
		}
		cmd = exec.Command(p, url)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
