// Command guard runs the project's architectural-drift guards.
// Invocation: `go run ./scripts/guard <check-name>` from server/.
//
// Available checks:
//
//	check-tts-voices    — service/tts/voices.go self-consistency:
//	                      unique IDs, non-empty providers, working
//	                      DefaultPresetID.
//	check-migrations    — every table / ADD COLUMN in migrations/
//	                      reflected in utils/testutil/testdb.go.
//	check-all           — runs the two above in sequence. Exits
//	                      with the worst status any check returned.
//
// Exit codes: 0 = healthy, 1 = drift detected, 2 = infra error.
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	name := os.Args[1]
	switch name {
	case "check-tts-voices":
		os.Exit(runCheckTTSVoices())
	case "check-migrations":
		os.Exit(runCheckMigrations())
	case "check-all":
		os.Exit(runAll())
	case "-h", "--help", "help":
		usage()
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "unknown check: %q\n\n", name)
		usage()
		os.Exit(2)
	}
}

// runAll runs every guard and returns the worst exit code observed.
// Individual failures don't short-circuit — the operator gets a
// consolidated report in one invocation.
func runAll() int {
	worst := 0
	for _, check := range []struct {
		name string
		run  func() int
	}{
		{"check-tts-voices", runCheckTTSVoices},
		{"check-migrations", runCheckMigrations},
	} {
		fmt.Printf("=== %s ===\n", check.name)
		code := check.run()
		if code > worst {
			worst = code
		}
		fmt.Println()
	}
	return worst
}

func usage() {
	fmt.Fprint(os.Stderr, `guard — architectural drift checks

usage:
  go run ./scripts/guard <check>

checks:
  check-tts-voices    service/tts/voices.go internal consistency
  check-migrations    migrations/ ↔ utils/testutil/testdb.go coverage
  check-all           run everything, report worst exit code

exit codes:
  0  healthy
  1  drift detected
  2  infra error (file not found, parse error, etc.)
`)
}
