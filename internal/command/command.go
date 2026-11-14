// Package command runs external command-line tools.
package command

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// maxErrorText caps how much tool output is quoted in an error.
const maxErrorText = 500

// Run executes name with args in dir and returns its standard output.
// env is added to the inherited environment.
func Run(ctx context.Context, dir string, env []string, name string, args ...string) (string, error) {
	return RunInput(ctx, dir, env, "", name, args...)
}

// RunInput is Run with input passed to the tool's standard input.
func RunInput(ctx context.Context, dir string, env []string, input string, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	if input != "" {
		cmd.Stdin = strings.NewReader(input)
	}
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		text := strings.TrimSpace(stderr.String())
		if text == "" {
			text = strings.TrimSpace(stdout.String())
		}
		if len(text) > maxErrorText {
			text = text[:maxErrorText] + "..."
		}
		if text == "" {
			return stdout.String(), err
		}
		return stdout.String(), fmt.Errorf("%w: %s", err, text)
	}
	return stdout.String(), nil
}

// Lines splits tool output into its non-empty lines.
func Lines(out string) []string {
	var lines []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}
