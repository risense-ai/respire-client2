package main

import "golang.org/x/sys/windows"

func exitDesktopProcess() error {
	// Wails and desktop defers have finished; ExitProcess can re-enter Go from DLL teardown (wails#6375).
	return windows.TerminateProcess(windows.CurrentProcess(), 0)
}
