package main

import "github.com/ut0mt8/mig-scaleset/internal/cli"

// Set at build time, see Makefile.
var version = "dev"

func main() {
	cli.Execute(version)
}
