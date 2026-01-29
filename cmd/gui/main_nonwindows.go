//go:build !windows

package main

func main() {
	// GUI is only supported on Windows builds.
	// Use cmd/cli on other platforms.
}
