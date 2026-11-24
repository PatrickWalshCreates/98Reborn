//go:build !linux

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "98reborn-installer runs on the 98Reborn installer medium (Linux) only.")
	os.Exit(1)
}
