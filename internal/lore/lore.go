// Package lore drives the lore command-line client in a working copy.
package lore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/KaosSpectrum/perforce2lore/internal/command"
)

// ChangelistKey is the revision metadata key Lore fills from LORE_P4_CHANGELIST.
const ChangelistKey = "p4-changelist"

// Client runs lore commands in one working copy.
type Client struct {
	Exe  string // lore executable; "lore" when empty
	Root string // working copy root
}

// Field is one metadata key and value.
type Field struct {
	Key   string
	Value string
}

// Event is one line of lore --json output.
type Event struct {
	Tag  string          `json:"tagName"`
	Data json.RawMessage `json:"data"`
}

// LastChangelist returns the changelist recorded on the current revision, or 0.
func (c Client) LastChangelist(ctx context.Context) (int, error) {
	evs, err := c.run(ctx, nil, "revision", "metadata", "get", ChangelistKey)
	if err != nil {
		return 0, err
	}
	for _, ev := range evs {
		if ev.Tag != "metadata" {
			continue
		}
		var m struct {
			Key   string `json:"key"`
			Value struct {
				Data string `json:"data"`
			} `json:"value"`
		}
		if json.Unmarshal(ev.Data, &m) == nil && m.Key == ChangelistKey {
			n, _ := strconv.Atoi(strings.TrimSpace(m.Value.Data))
			return n, nil
		}
	}
	return 0, nil
}

// Stage stages every change in the working copy and returns how many entries were staged.
func (c Client) Stage(ctx context.Context) (int, error) {
	evs, err := c.run(ctx, nil, "stage", "--scan", ".")
	if err != nil {
		return 0, err
	}
	for _, ev := range evs {
		if ev.Tag != "fileStageEnd" {
			continue
		}
		var e struct {
			Count struct {
				Total int `json:"totalCount"`
			} `json:"count"`
		}
		if json.Unmarshal(ev.Data, &e) == nil {
			return e.Count.Total, nil
		}
	}
	return 0, nil
}

// Status is the part of lore status the importer uses.
type Status struct {
	Staged int  // staged entries, including any left by a failed commit
	Ahead  bool // the branch has revisions the remote does not
}

// Status reads the working copy's status.
func (c Client) Status(ctx context.Context) (Status, error) {
	evs, err := c.run(ctx, nil, "status")
	if err != nil {
		return Status{}, err
	}
	return ParseStatus(evs), nil
}

// ParseStatus reads staged entries and the ahead flag from status events.
func ParseStatus(evs []Event) Status {
	var s Status
	for _, ev := range evs {
		switch ev.Tag {
		case "repositoryStatusFile":
			var f struct {
				Staged bool `json:"flagStaged"`
			}
			if json.Unmarshal(ev.Data, &f) == nil && f.Staged {
				s.Staged++
			}
		case "repositoryStatusRevision":
			var r struct {
				Ahead int `json:"isLocalAhead"`
			}
			if json.Unmarshal(ev.Data, &r) == nil {
				s.Ahead = r.Ahead > 0
			}
		}
	}
	return s
}

// Commit sets metadata on the staged revision and commits it, recording changelist as p4-changelist.
func (c Client) Commit(ctx context.Context, changelist int, message string, metadata []Field) error {
	if len(metadata) > 0 {
		args := []string{"revision", "metadata", "set", "--"}
		for _, f := range metadata {
			args = append(args, f.Key, f.Value)
		}
		if _, err := c.run(ctx, nil, args...); err != nil {
			return err
		}
	}
	env := []string{"LORE_P4_CHANGELIST=" + strconv.Itoa(changelist)}
	_, err := c.run(ctx, env, "revision", "commit", "--", message)
	return err
}

// Branch is one branch of the repository.
type Branch struct {
	Name    string
	Latest  string // newest revision, local when the clone has one, else remote
	Current bool   // the working copy is on this branch
}

// zeroRevision is the signature Lore reports for no revision.
const zeroRevision = "0000000000000000000000000000000000000000000000000000000000000000"

// Branches lists local and remote branches by name.
func (c Client) Branches(ctx context.Context) (map[string]Branch, error) {
	evs, err := c.run(ctx, nil, "branch", "list")
	if err != nil {
		return nil, err
	}
	return ParseBranches(evs), nil
}

// ParseBranches merges local and remote branch list entries by name.
func ParseBranches(evs []Event) map[string]Branch {
	branches := map[string]Branch{}
	for _, ev := range evs {
		if ev.Tag != "branchListEntry" {
			continue
		}
		var e struct {
			Location string `json:"location"`
			Name     string `json:"name"`
			Latest   string `json:"latest"`
			Current  bool   `json:"isCurrent"`
		}
		if json.Unmarshal(ev.Data, &e) != nil || e.Name == "" {
			continue
		}
		b := branches[e.Name]
		b.Name = e.Name
		b.Current = b.Current || e.Current
		if e.Latest == zeroRevision {
			e.Latest = ""
		}
		if (e.Location == "local" && e.Latest != "") || b.Latest == "" {
			b.Latest = e.Latest
		}
		branches[e.Name] = b
	}
	return branches
}

// RevisionMetadata returns the metadata of a revision.
func (c Client) RevisionMetadata(ctx context.Context, revision string) (map[string]string, error) {
	evs, err := c.run(ctx, nil, "revision", "metadata", "get", "--revision", revision)
	if err != nil {
		return nil, err
	}
	metadata := map[string]string{}
	for _, ev := range evs {
		if ev.Tag != "metadata" {
			continue
		}
		var m struct {
			Key   string `json:"key"`
			Value struct {
				Data string `json:"data"`
			} `json:"value"`
		}
		if json.Unmarshal(ev.Data, &m) == nil && m.Key != "" {
			metadata[m.Key] = strings.TrimSpace(m.Value.Data)
		}
	}
	return metadata, nil
}

// FindChangelist returns the revision on branch that recorded changelist n, or "" when there is none.
func (c Client) FindChangelist(ctx context.Context, branch string, n int) (string, error) {
	evs, err := c.run(ctx, nil, "revision", "find", "metadata", "--branch", branch, ChangelistKey, strconv.Itoa(n))
	if err != nil {
		if strings.Contains(err.Error(), "no revision found") {
			return "", nil
		}
		return "", err
	}
	for _, ev := range evs {
		var f struct {
			Signature string `json:"signature"`
		}
		if ev.Tag == "revisionFind" && json.Unmarshal(ev.Data, &f) == nil && f.Signature != zeroRevision {
			return f.Signature, nil
		}
	}
	return "", nil
}

// SyncTo updates the working copy to a revision.
func (c Client) SyncTo(ctx context.Context, revision string) error {
	_, err := c.run(ctx, nil, "revision", "sync", revision)
	return err
}

// CreateBranch creates a branch at the working copy's revision and moves the working copy onto it.
func (c Client) CreateBranch(ctx context.Context, name string) error {
	_, err := c.run(ctx, nil, "branch", "create", name)
	return err
}

// SwitchBranch moves the working copy to the newest revision of a branch.
func (c Client) SwitchBranch(ctx context.Context, name string) error {
	_, err := c.run(ctx, nil, "branch", "switch", name)
	return err
}

// IsWorkingCopy reports whether Root holds a Lore working copy.
func (c Client) IsWorkingCopy() bool {
	info, err := os.Stat(filepath.Join(c.Root, ".lore"))
	return err == nil && info.IsDir()
}

// Clone clones the repository at url into Root.
func (c Client) Clone(ctx context.Context, url string) error {
	parent := filepath.Dir(c.Root)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	out, err := command.Run(ctx, parent, nil, c.exe(), "--json", "-P", "--non-interactive", "clone", url, c.Root)
	if err != nil {
		if msg := CompletionError(ParseEvents(out), exitCode(err)); msg != "" {
			return fmt.Errorf("lore clone: %s", msg)
		}
		return fmt.Errorf("lore clone: %w", err)
	}
	return nil
}

// Push sends local revisions to the remote.
func (c Client) Push(ctx context.Context) error {
	_, err := c.run(ctx, nil, "push")
	return err
}

// run executes a lore command with --json and returns its events.
// A failing command reports the message from its completion event.
func (c Client) run(ctx context.Context, env []string, args ...string) ([]Event, error) {
	full := append([]string{"--json", "-P", "--non-interactive", "--repository", c.Root}, args...)
	out, runErr := command.Run(ctx, c.Root, env, c.exe(), full...)
	evs := ParseEvents(out)
	if runErr == nil {
		return evs, nil
	}
	if msg := CompletionError(evs, exitCode(runErr)); msg != "" {
		return evs, fmt.Errorf("lore %s: %s", args[0], msg)
	}
	return evs, fmt.Errorf("lore %s: %w", args[0], runErr)
}

func (c Client) exe() string {
	if c.Exe == "" {
		return "lore"
	}
	return c.Exe
}

// exitCode returns a failed command's exit code, or -1 when it did not run.
func exitCode(err error) int {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

// ParseEvents decodes lore --json output, skipping lines that are not events.
func ParseEvents(out string) []Event {
	var evs []Event
	for _, line := range command.Lines(out) {
		var ev Event
		if json.Unmarshal([]byte(line), &ev) == nil && ev.Tag != "" {
			evs = append(evs, ev)
		}
	}
	return evs
}

// CompletionError returns the error message of the completion event with the given exit code.
func CompletionError(evs []Event, code int) string {
	for _, ev := range evs {
		if ev.Tag != "complete" {
			continue
		}
		var c struct {
			Status int `json:"status"`
			Error  struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(ev.Data, &c) == nil && c.Status == code && c.Error.Message != "" {
			return fmt.Sprintf("%s (%d)", c.Error.Message, c.Status)
		}
	}
	return ""
}
