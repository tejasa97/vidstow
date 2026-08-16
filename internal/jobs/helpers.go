// helpers.go: small os and log shims so we can stub them in tests.
package jobs

import (
	"log"
	"os"
)

var (
	mkdirAll    = os.MkdirAll
	userHomeDir = os.UserHomeDir
	// logRetiredSessionLeak reports a best-effort discard that failed after a
	// retry escalation commit. It must receive no paths or credentials: the
	// shim exists so tests can assert exactly that.
	logRetiredSessionLeak = log.Printf
)
