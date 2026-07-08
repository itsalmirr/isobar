// Command clockcheck is a standalone runner for the clockcheck analyzer
// (see github.com/wh0o0pz/isobar/tools/clockcheck). It is wired into
// `make lint` so a stray time.Now in internal/* fails the build per
// ADR-0012.
package main

import (
	"golang.org/x/tools/go/analysis/singlechecker"

	"github.com/wh0o0pz/isobar/tools/clockcheck"
)

func main() {
	singlechecker.Main(clockcheck.Analyzer)
}
