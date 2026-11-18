// Command harbor is the whole product: one process serving the API, the
// embedded SvelteKit build and the CLI subcommands the image and the update
// watcher call (design.md §3).
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/AutumnsGrove/Harbor/server/internal/build"
)

const usage = `harbor — one binary, one process.

Usage: harbor <command>

Commands:
  version   print the build stamp
  help      print this text
`

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "harbor:", err)
		os.Exit(1)
	}
}

// run is main without the process, so every command is testable.
func run(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("no command given\n\n%s", usage)
	}
	switch args[0] {
	case "version":
		fmt.Fprintln(stdout, build.Version())
		return nil
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return nil
	default:
		return fmt.Errorf("unknown command %q\n\n%s", args[0], usage)
	}
}
