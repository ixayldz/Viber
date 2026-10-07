package main

import (
	"github.com/ixayldz/Viber/internal/cli"
	"os"
)

func main() { os.Exit(cli.Execute(os.Args[1:], os.Stdout, os.Stderr)) }
