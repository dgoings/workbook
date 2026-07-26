package main

import (
	"fmt"
	"os"

	"github.com/dgoings/workbook/internal/release"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: workbook-release-archive <binary> <archive>")
		os.Exit(2)
	}
	if err := release.WriteExecutableArchive(os.Args[1], os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
