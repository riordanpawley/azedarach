package main

import (
	"fmt"
	"os"
	"testing"

	"github.com/riordanpawley/azedarach/internal/testisolation"
)

func TestMain(m *testing.M) {
	environment, err := testisolation.NewTemporary(".")
	if err != nil {
		panic(err)
	}
	restore, err := environment.Apply()
	if err != nil {
		panic(err)
	}
	code := m.Run()
	restore()
	if err := environment.Close(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "remove command test isolation: %v\n", err)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}
