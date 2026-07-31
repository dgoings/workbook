// Command workbook-release-formula renders the Workbook Homebrew formula.
//
// It exists so that scripts/render-homebrew-formula.sh and the Go release
// package share one formula template instead of maintaining byte-identical
// copies that can drift apart.
package main

import (
	"fmt"
	"os"

	"github.com/dgoings/workbook/internal/release"
)

func main() {
	if len(os.Args) != 7 {
		fmt.Fprintln(os.Stderr, "usage: workbook-release-formula <version> <darwin-arm64-sha256> <darwin-amd64-sha256> <linux-arm64-sha256> <linux-amd64-sha256> <repository>")
		os.Exit(2)
	}
	formula, err := release.RenderFormula(os.Args[1], os.Args[6], release.FormulaArchives{
		DarwinARM64: os.Args[2],
		DarwinAMD64: os.Args[3],
		LinuxARM64:  os.Args[4],
		LinuxAMD64:  os.Args[5],
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if _, err := os.Stdout.WriteString(formula); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
