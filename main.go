package main

import "github.com/ut0mt8/mig-scaleset/cmd"

// Set at build time, see Makefile.
var version = "dev"

func main() {
	cmd.Execute(version)
}
