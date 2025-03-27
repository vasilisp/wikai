package util

import (
	"fmt"
	"regexp"
)

var pageBasePathRegex = regexp.MustCompile(`^[a-zA-Z0-9]+(?:-[a-zA-Z0-9]+)*$`)

func ValidatePagePath(path string) error {
	if !pageBasePathRegex.MatchString(path) {
		return fmt.Errorf("invalid path: %s", path)
	}
	return nil
}
