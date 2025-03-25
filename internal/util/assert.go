package util

import "log"

// Assert panics with the given message if the condition is false
func Assert(condition bool, msg string) {
	if !condition {
		log.Fatalf("Assertion failed: %s", msg)
	}
}
