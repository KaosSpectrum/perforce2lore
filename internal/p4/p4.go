// Package p4 reads submitted changelists from a Perforce server and syncs a workspace.
package p4

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/KaosSpectrum/perforce2lore/internal/command"
)

// Client runs p4 commands against one server and workspace.
type Client struct {
	Exe       string // p4 executable; "p4" when empty
	Port      string // empty uses P4PORT or P4CONFIG
	User      string // empty uses P4USER or P4CONFIG
	Workspace string // empty uses P4CLIENT or P4CONFIG
	Charset   string // empty uses P4CHARSET or the server default
	Dir       string // working directory for p4 commands

	// SyncThreads above 1 syncs in parallel; the server must allow net.parallel.max.
	SyncThreads int
}

// Change is one submitted changelist.
type Change struct {
	Number      int
	User        string
	Workspace   string // workspace it was submitted from
	Time        int64  // submit time, Unix seconds
	Description string
}

// User is one Perforce account.
type User struct {
	Name     string
	Email    string
	FullName string
}

// WorkspaceSpec is the part of a workspace definition the importer checks.
type WorkspaceSpec struct {
	Name    string
	Root    string
	Stream  string // empty for a workspace without a stream
	Options []string
	View    []string
}

// Stream is one stream definition.
type Stream struct {
	Path   string // //depot/name
	Parent string // "none" for a mainline
	Type   string // mainline, development, release, virtual, task
}

// HasOption reports whether the workspace has an option such as "rmdir".
func (w WorkspaceSpec) HasOption(option string) bool {
	for _, o := range w.Options {
		if strings.EqualFold(o, option) {
			return true
		}
	}
	return false
}

// Record is one tagged output record from p4 -ztag -Mj.
type Record map[string]any

// String returns a field as text; missing or non-text fields are empty.
func (r Record) String(key string) string {
	s, _ := r[key].(string)
	return s
}

// Changes lists submitted changelists under depotPath above after, oldest first.
func (c Client) Changes(ctx context.Context, depotPath string, after int) ([]Change, error) {
	path := depotPath
	if after > 0 {
		path += fmt.Sprintf("@%d,@now", after+1)
	}
	recs, err := c.run(ctx, "changes", "-s", "submitted", "-l", path)
	if err != nil {
		return nil, err
	}
	return ParseChanges(recs), nil
}

// ParseChanges converts changes records into changelists, oldest first.
func ParseChanges(recs []Record) []Change {
	changes := make([]Change, 0, len(recs))
	for _, r := range recs {
		n, err := strconv.Atoi(r.String("change"))
		if err != nil {
			continue
		}
		t, _ := strconv.ParseInt(r.String("time"), 10, 64)
		changes = append(changes, Change{
			Number:      n,
			User:        r.String("user"),
			Workspace:   r.String("client"),
			Time:        t,
			Description: r.String("desc"),
		})
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Number < changes[j].Number })
	return changes
}

// Sync brings the workspace's copy of depotPath to changelist n.
func (c Client) Sync(ctx context.Context, depotPath string, n int) error {
	args := []string{"-q"}
	if c.SyncThreads > 1 {
		args = append(args, fmt.Sprintf("--parallel=threads=%d", c.SyncThreads))
	}
	_, err := c.run(ctx, "sync", append(args, fmt.Sprintf("%s@%d", depotPath, n))...)
	return err
}

// Spec returns the workspace definition.
func (c Client) Spec(ctx context.Context) (WorkspaceSpec, error) {
	recs, err := c.run(ctx, "client", "-o")
	if err != nil {
		return WorkspaceSpec{}, err
	}
	if len(recs) == 0 {
		return WorkspaceSpec{}, fmt.Errorf("p4 client: no workspace returned")
	}
	r := recs[0]
	spec := WorkspaceSpec{
		Name:    r.String("Client"),
		Root:    r.String("Root"),
		Stream:  r.String("Stream"),
		Options: strings.Fields(r.String("Options")),
	}
	for i := 0; ; i++ {
		line, ok := r["View"+strconv.Itoa(i)].(string)
		if !ok {
			break
		}
		spec.View = append(spec.View, line)
	}
	return spec, nil
}

// Maps reports whether the workspace view is exactly depotPath mapped to the workspace root.
func (w WorkspaceSpec) Maps(depotPath string) bool {
	return len(w.View) == 1 && viewDepotSide(w.View[0]) == depotPath
}

// viewDepotSide returns the depot side of a view line, which may be quoted.
func viewDepotSide(line string) string {
	line = strings.TrimSpace(line)
	if rest, ok := strings.CutPrefix(line, "\""); ok {
		depot, _, _ := strings.Cut(rest, "\"")
		return depot
	}
	depot, _, _ := strings.Cut(line, " ")
	return depot
}

// viewLine maps depotPath to the whole of workspace's root.
func viewLine(depotPath, workspace string) string {
	return quote(depotPath) + " " + quote("//"+workspace+"/...")
}

func quote(s string) string {
	if strings.ContainsAny(s, " \t") {
		return "\"" + s + "\""
	}
	return s
}

// WorkspaceExists reports whether the workspace this client uses exists on the server.
func (c Client) WorkspaceExists(ctx context.Context) (bool, error) {
	recs, err := c.run(ctx, "clients", "-e", c.Workspace)
	if err != nil {
		return false, err
	}
	for _, r := range recs {
		if r.String("client") == c.Workspace {
			return true, nil
		}
	}
	return false, nil
}

// CreateStreamWorkspace creates this client's workspace on stream with the given root.
// The rmdir and clobber options are turned on so syncs mirror deletes and overwrite stale files.
func (c Client) CreateStreamWorkspace(ctx context.Context, stream, root string) error {
	exe := c.exe()
	base := c.connectionArgs()
	spec, err := command.Run(ctx, c.Dir, nil, exe, append(base, "client", "-S", stream, "-o", c.Workspace)...)
	if err != nil {
		return fmt.Errorf("p4 client -o: %w", err)
	}
	spec = SetSpecField(spec, "Root", root)
	spec = SetSpecField(spec, "Options", workspaceOptions(SpecField(spec, "Options")))
	if _, err := command.RunInput(ctx, c.Dir, nil, spec, exe, append(base, "client", "-i")...); err != nil {
		return fmt.Errorf("p4 client -i: %w", err)
	}
	return nil
}

// CreateWorkspace creates this client's classic workspace mapping depotPath to root.
// The rmdir and clobber options are turned on so syncs mirror deletes and overwrite stale files.
func (c Client) CreateWorkspace(ctx context.Context, depotPath, root string) error {
	return c.editSpec(ctx, func(spec string) string {
		spec = SetSpecField(spec, "Root", root)
		spec = SetSpecField(spec, "Options", workspaceOptions(SpecField(spec, "Options")))
		return SetSpecBlock(spec, "View", []string{viewLine(depotPath, c.Workspace)})
	})
}

// SetView maps depotPath to this client's classic workspace root, replacing its view; files on disk are not changed.
func (c Client) SetView(ctx context.Context, depotPath string) error {
	return c.editSpec(ctx, func(spec string) string {
		return SetSpecBlock(spec, "View", []string{viewLine(depotPath, c.Workspace)})
	})
}

// editSpec reads this client's workspace spec, changes it and saves it.
func (c Client) editSpec(ctx context.Context, change func(string) string) error {
	base := c.connectionArgs()
	spec, err := command.Run(ctx, c.Dir, nil, c.exe(), append(base, "client", "-o", c.Workspace)...)
	if err != nil {
		return fmt.Errorf("p4 client -o: %w", err)
	}
	if _, err := command.RunInput(ctx, c.Dir, nil, change(spec), c.exe(), append(base, "client", "-i")...); err != nil {
		return fmt.Errorf("p4 client -i: %w", err)
	}
	return nil
}

// SwitchStream points this client's workspace at another stream; files on disk are not changed.
func (c Client) SwitchStream(ctx context.Context, stream string) error {
	_, err := c.run(ctx, "client", "-s", "-S", stream)
	return err
}

// Streams lists the streams matching pattern, such as //Game/...
func (c Client) Streams(ctx context.Context, pattern string) ([]Stream, error) {
	recs, err := c.run(ctx, "streams", pattern)
	if err != nil {
		return nil, err
	}
	streams := make([]Stream, 0, len(recs))
	for _, r := range recs {
		if s := r.String("Stream"); s != "" {
			streams = append(streams, Stream{Path: s, Parent: r.String("Parent"), Type: r.String("Type")})
		}
	}
	return streams, nil
}

// ChangesUpTo lists at most limit submitted changelists under depotPath at or below n, newest first.
func (c Client) ChangesUpTo(ctx context.Context, depotPath string, n, limit int) ([]int, error) {
	recs, err := c.run(ctx, "changes", "-s", "submitted", "-m", strconv.Itoa(limit), fmt.Sprintf("%s@%d", depotPath, n))
	if err != nil {
		return nil, err
	}
	changes := ParseChanges(recs)
	numbers := make([]int, len(changes))
	for i, ch := range changes {
		numbers[len(changes)-1-i] = ch.Number
	}
	return numbers, nil
}

// Flush records depotPath at changelist n as synced without changing files; n of 0 records nothing synced.
func (c Client) Flush(ctx context.Context, depotPath string, n int) error {
	rev := "#none"
	if n > 0 {
		rev = fmt.Sprintf("@%d", n)
	}
	_, err := c.run(ctx, "sync", "-k", "-q", depotPath+rev)
	return err
}

// Differing lists the depot files under depotPath whose workspace copy is changed or missing.
func (c Client) Differing(ctx context.Context, depotPath string) ([]string, error) {
	var files []string
	for _, flag := range []string{"-se", "-sd"} {
		recs, err := c.run(ctx, "diff", flag, depotPath)
		if err != nil {
			return nil, err
		}
		for _, r := range recs {
			if f := r.String("depotFile"); f != "" {
				files = append(files, f)
			}
		}
	}
	return files, nil
}

// forceSyncBatch is how many files one forced sync names.
const forceSyncBatch = 200

// ForceSync rewrites the given depot files at changelist n.
func (c Client) ForceSync(ctx context.Context, files []string, n int) error {
	for start := 0; start < len(files); start += forceSyncBatch {
		args := []string{"-f", "-q"}
		for _, f := range files[start:min(start+forceSyncBatch, len(files))] {
			args = append(args, fmt.Sprintf("%s@%d", f, n))
		}
		if _, err := c.run(ctx, "sync", args...); err != nil {
			return err
		}
	}
	return nil
}

// Have lists the local paths of the files synced under depotPath.
func (c Client) Have(ctx context.Context, depotPath string) ([]string, error) {
	recs, err := c.run(ctx, "have", depotPath)
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(recs))
	for _, r := range recs {
		if p := r.String("path"); p != "" {
			paths = append(paths, p)
		}
	}
	return paths, nil
}

// SpecField returns a single-line field of a form-style spec.
func SpecField(spec, field string) string {
	for _, line := range strings.Split(spec, "\n") {
		if value, ok := strings.CutPrefix(line, field+":"); ok {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// SetSpecField replaces a single-line field of a form-style spec.
func SetSpecField(spec, field, value string) string {
	lines := strings.Split(spec, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, field+":") {
			lines[i] = field + ":\t" + value
			return strings.Join(lines, "\n")
		}
	}
	return spec + "\n" + field + ":\t" + value + "\n"
}

// SetSpecBlock replaces a multi-line field of a form-style spec with the given lines.
func SetSpecBlock(spec, field string, lines []string) string {
	var out []string
	in := false
	for _, line := range strings.Split(strings.ReplaceAll(spec, "\r\n", "\n"), "\n") {
		if in {
			if strings.HasPrefix(line, "\t") || strings.HasPrefix(line, " ") {
				continue
			}
			in = false
		}
		if strings.HasPrefix(line, field+":") {
			in = true
			continue
		}
		out = append(out, line)
	}
	block := []string{field + ":"}
	for _, l := range lines {
		block = append(block, "\t"+l)
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n") + "\n\n" + strings.Join(block, "\n") + "\n"
}

// workspaceOptions turns on rmdir and clobber in a workspace options line.
func workspaceOptions(options string) string {
	fields := strings.Fields(options)
	for i, f := range fields {
		switch f {
		case "normdir":
			fields[i] = "rmdir"
		case "noclobber":
			fields[i] = "clobber"
		}
	}
	return strings.Join(fields, " ")
}

// Users returns every account, keyed by lower-case user name.
func (c Client) Users(ctx context.Context) (map[string]User, error) {
	recs, err := c.run(ctx, "users")
	if err != nil {
		return nil, err
	}
	users := make(map[string]User, len(recs))
	for _, r := range recs {
		u := User{Name: r.String("User"), Email: r.String("Email"), FullName: r.String("FullName")}
		if u.Name != "" {
			users[strings.ToLower(u.Name)] = u
		}
	}
	return users, nil
}

// Jobs returns the jobs fixed by changelist n.
func (c Client) Jobs(ctx context.Context, n int) ([]string, error) {
	recs, err := c.run(ctx, "fixes", "-c", strconv.Itoa(n))
	if err != nil {
		return nil, err
	}
	var jobs []string
	for _, r := range recs {
		if job := r.String("Job"); job != "" {
			jobs = append(jobs, job)
		}
	}
	return jobs, nil
}

// CheckLogin fails when the session has no valid ticket.
func (c Client) CheckLogin(ctx context.Context) error {
	_, err := c.run(ctx, "login", "-s")
	return err
}

func (c Client) exe() string {
	if c.Exe == "" {
		return "p4"
	}
	return c.Exe
}

func (c Client) globalArgs() []string {
	return append([]string{"-ztag", "-Mj"}, c.connectionArgs()...)
}

// connectionArgs selects the server, user and charset; the workspace is added when set.
func (c Client) connectionArgs() []string {
	var args []string
	if c.Port != "" {
		args = append(args, "-p", c.Port)
	}
	if c.User != "" {
		args = append(args, "-u", c.User)
	}
	if c.Workspace != "" {
		args = append(args, "-c", c.Workspace)
	}
	if c.Charset != "" {
		args = append(args, "-C", c.Charset)
	}
	return args
}

// run executes a p4 command and returns its data records; error records fail it, warnings are dropped.
func (c Client) run(ctx context.Context, cmd string, args ...string) ([]Record, error) {
	out, runErr := command.Run(ctx, c.Dir, nil, c.exe(), append(c.globalArgs(), append([]string{cmd}, args...)...)...)
	recs, failure, parseErr := ParseRecords(out)
	switch {
	case failure != "":
		return nil, fmt.Errorf("p4 %s: %s", cmd, failure)
	case runErr != nil:
		return nil, fmt.Errorf("p4 %s: %w", cmd, runErr)
	case parseErr != nil:
		return nil, fmt.Errorf("p4 %s: %w", cmd, parseErr)
	}
	return recs, nil
}

// severityFailed is the lowest p4 message severity that is an error rather than a warning.
const severityFailed = 3

// ParseRecords splits p4 -ztag -Mj output into data records and the first error message.
func ParseRecords(out string) (recs []Record, failure string, err error) {
	for _, line := range command.Lines(out) {
		rec := Record{}
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			return nil, "", fmt.Errorf("unreadable output: %w", err)
		}
		if rec.String("code") != "error" {
			recs = append(recs, rec)
			continue
		}
		severity, hasSeverity := rec["severity"].(float64)
		if (!hasSeverity || severity >= severityFailed) && failure == "" {
			failure = strings.TrimSpace(rec.String("data"))
		}
	}
	return recs, failure, nil
}
