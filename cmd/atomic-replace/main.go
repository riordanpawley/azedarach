package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: atomic-replace <source> <destination>")
		os.Exit(2)
	}
	if err := os.Rename(os.Args[1], os.Args[2]); err != nil {
		fmt.Fprintf(os.Stderr, "atomically replace %s with %s: %v\n", os.Args[2], os.Args[1], err)
		os.Exit(1)
	}
}
