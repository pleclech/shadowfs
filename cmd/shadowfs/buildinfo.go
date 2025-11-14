package main

import (
	"fmt"
	"os"
)

// Version information set at build time via ldflags
var (
	Version   = "dev"
	Commit    = "unknown"
	BuildDate = "unknown"
)

// PrintVersion prints version information and exits
func PrintVersion() {
	fmt.Printf("shadowfs version %s\n", Version)
	fmt.Printf("Commit: %s\n", Commit)
	fmt.Printf("Built: %s\n", BuildDate)
	os.Exit(0)
}

