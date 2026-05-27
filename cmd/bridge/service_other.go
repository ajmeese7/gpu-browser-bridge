//go:build !windows

package main

func runService() {
	fatal("service mode is Windows-only")
}
