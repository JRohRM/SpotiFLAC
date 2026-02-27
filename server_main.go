//go:build server
// +build server

package main

// When built with -tags server, this replaces the Wails main() entirely.
// No imports needed — StartServer() is in server.go (same package).
func main() {
	StartServer()
}
