package coderepair

import (
	"errors"
	"path"
	"regexp"
	"strings"
)

// ValidationProfile is admin-owned, pinned repository configuration. The model
// selects neither the image nor executable/arguments. Images must be prebuilt.
type ValidationProfile struct {
	ID               string              `json:"id"`
	Version          string              `json:"version"`
	Image            string              `json:"image"`
	RootFiles        []string            `json:"root_files"`
	ProtectedPaths   []string            `json:"protected_paths"`
	Checks           []ValidationCommand `json:"checks"`
	Test             ValidationCommand   `json:"test"`
	ExpectedTestName string              `json:"expected_test_name"`
	ExpectedFailure  string              `json:"expected_failure"`
}

type ValidationCommand struct {
	Executable         string   `json:"executable"`
	Arguments          []string `json:"arguments"`
	TimeoutSeconds     int      `json:"timeout_seconds"`
	RequireEmptyOutput bool     `json:"require_empty_output,omitempty"`
}

var sandboxImage = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@-]{0,200}$`)

func (p ValidationProfile) Validate() error {
	if !validateRecipeID(p.ID) || !validateRecipeID(p.Version) || !sandboxImage.MatchString(p.Image) || strings.Contains(p.Image, "..") ||
		len(p.RootFiles) == 0 || len(p.RootFiles) > 16 || len(p.ProtectedPaths) == 0 || len(p.ProtectedPaths) > 32 || len(p.Checks) > 4 {
		return errors.New("validation profile requires a versioned prebuilt image, root files and protected test paths")
	}
	for _, name := range append(append([]string{}, p.RootFiles...), p.ProtectedPaths...) {
		if ValidateRepoPath(name) != nil {
			return errors.New("validation profile has an invalid repository path")
		}
	}
	if p.ExpectedTestName == "" || len(p.ExpectedTestName) > 128 || p.ExpectedFailure == "" || len(p.ExpectedFailure) > 512 ||
		strings.ContainsAny(p.ExpectedTestName+p.ExpectedFailure, "\r\n\x00") {
		return errors.New("validation profile requires bounded regression evidence")
	}
	for _, command := range append(append([]ValidationCommand{}, p.Checks...), p.Test) {
		if err := command.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func (c ValidationCommand) Validate() error {
	if !strings.HasPrefix(c.Executable, "/") || path.Clean(c.Executable) != c.Executable || strings.ContainsAny(c.Executable, "\r\n\x00\\ ") ||
		len(c.Executable) > 256 || c.TimeoutSeconds < 1 || c.TimeoutSeconds > 300 || len(c.Arguments) > 32 {
		return errors.New("validation command requires an absolute container executable and bounded arguments/deadline")
	}
	switch strings.ToLower(path.Base(c.Executable)) {
	case "sh", "bash", "zsh", "dash", "ash", "cmd", "powershell", "pwsh", "docker", "curl", "wget":
		return errors.New("validation commands cannot launch a shell or infrastructure tool")
	}
	for _, argument := range c.Arguments {
		if len(argument) > 512 || strings.ContainsAny(argument, "\r\n\x00") || argument == "-c" || argument == "-e" || argument == "--eval" {
			return errors.New("validation arguments cannot contain inline programs or control characters")
		}
	}
	return nil
}

func (b RepositoryBinding) CanPatchPath(name string) bool {
	if !b.AllowsPath(name) {
		return false
	}
	if b.ValidationProfile != nil {
		for _, prefix := range append(append([]string{}, b.ValidationProfile.ProtectedPaths...), b.ValidationProfile.RootFiles...) {
			if name == prefix || strings.HasPrefix(name, prefix+"/") {
				return false
			}
		}
		return true
	}
	// Legacy Go fixtures have no profile; retain their protected regression files.
	return !strings.HasSuffix(name, "_test.go")
}
