package coderepair

import (
	"errors"
	"path/filepath"
	"regexp"
	"strings"
)

var credentialEnvironmentName = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)

func ValidateCredentialReference(ref string) error {
	if ref == "" {
		return nil
	}
	if strings.HasPrefix(ref, "env:") && credentialEnvironmentName.MatchString(strings.TrimPrefix(ref, "env:")) {
		return nil
	}
	if strings.HasPrefix(ref, "file:") {
		name := strings.TrimPrefix(ref, "file:")
		if filepath.IsAbs(name) && filepath.Clean(name) == name && len(name) <= 512 && !strings.ContainsAny(name, "\r\n\x00") {
			return nil
		}
	}
	return errors.New("GitHub credential reference must be env:NAME or file:absolute-path")
}
