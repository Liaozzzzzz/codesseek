//go:build windows

package main

import (
	"os"
	"syscall"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

// A Chinese Windows console defaults to code page 936 (GBK). Writing UTF-8
// bytes to it mangles snippets, so encode stdout to the console code page when
// — and only when — stdout is a terminal. Redirected output and --json stay
// UTF-8.
func init() {
	if info, err := os.Stdout.Stat(); err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return
	}
	switch consoleOutputCP() {
	case 936:
		stdout = transform.NewWriter(stdout, simplifiedchinese.GBK.NewEncoder())
	case 54936:
		stdout = transform.NewWriter(stdout, simplifiedchinese.GB18030.NewEncoder())
	}
}

func consoleOutputCP() uint32 {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	getCP := kernel32.NewProc("GetConsoleOutputCP")
	value, _, _ := getCP.Call()
	return uint32(value)
}
