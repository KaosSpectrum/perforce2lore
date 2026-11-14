// Package importer replays Perforce changelists into a Lore working copy, one revision each.
package importer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/KaosSpectrum/perforce2lore/internal/lore"
	"github.com/KaosSpectrum/perforce2lore/internal/p4"
)

// branchPointSearch is how many parent changelists are tried when looking for a branch point.
const branchPointSearch = 50

// errWaiting skips a stream until its parent has been imported far enough.
var errWaiting = errors.New("waiting for the parent stream")

// Importer imports Perforce changelists into a Lore repository.
type Importer struct {
	cfg      *Config
	p4       p4.Client
	lore     lore.Client
	state    *state
	users    map[string]p4.User
	unpushed int
}

// New creates an importer for a loaded configuration.
func New(cfg *Config) *Importer {
	return &Importer{
		cfg: cfg,
		p4: p4.Client{
			Exe: cfg.P4Exe, Port: cfg.P4Port, User: cfg.P4User, Workspace: cfg.P4Client,
			Charset: cfg.P4Charset, Dir: cfg.LoreRoot, SyncThreads: cfg.SyncThreads,
		},
		lore: lore.Client{Exe: cfg.LoreExe, Root: cfg.LoreRoot},
	}
}

// Lock prevents two importers from running with the same state file.
func (im *Importer) Lock() (io.Closer, error) {
	return acquireLock(im.cfg.StateFile + ".lock")
}

// Check verifies the Perforce login, the main workspace, the Lore working copy and the stream selection.
// It returns warnings for settings that work but need attention.
func (im *Importer) Check(ctx context.Context) ([]string, error) {
	if err := im.p4.CheckLogin(ctx); err != nil {
		return nil, err
	}
	spec, err := im.p4.Spec(ctx)
	if err != nil {
		return nil, err
	}
	if !samePath(spec.Root, im.cfg.LoreRoot) {
		return nil, fmt.Errorf("workspace %q root is %q, not loreRoot %q", spec.Name, spec.Root, im.cfg.LoreRoot)
	}
	if _, err := im.lore.Status(ctx); err != nil {
		return nil, err
	}
	var warnings []string
	if !spec.HasOption("rmdir") && !im.cfg.pruneEmptyDirs() {
		warnings = append(warnings, fmt.Sprintf("workspace %q has no rmdir option and pruneEmptyDirs is off: folders emptied by deletes stay in Lore", spec.Name))
	}
	switch {
	case im.cfg.Streams != nil && spec.Stream == "":
		return nil, fmt.Errorf("workspace %q has no stream; importing streams needs a stream workspace", spec.Name)
	case im.cfg.Paths != nil && spec.Stream != "":
		return nil, fmt.Errorf("workspace %q is a stream workspace; importing classic paths needs a classic workspace", spec.Name)
	}
	if im.cfg.layout() != nil {
		if _, err := im.targets(ctx); err != nil {
			return nil, err
		}
	}
	return warnings, nil
}

// position is how far a target has been imported.
type position struct {
	exists bool // the branch exists
	last   int  // newest changelist imported
	own    bool // the branch's newest revision was imported from this target
}

// position reads a target's progress from the state file and its branch's newest revision.
func (im *Importer) position(ctx context.Context, t *target) (position, error) {
	saved := im.state.last(t.source)
	if t.source == "" {
		recorded, err := im.lore.LastChangelist(ctx)
		if err != nil {
			return position{}, err
		}
		return position{exists: true, last: max(saved, recorded, im.cfg.StartChange-1), own: true}, nil
	}
	wc := im.workingCopy(t)
	branches, err := wc.Branches(ctx)
	if err != nil {
		return position{}, err
	}
	isRoot := t.parent == nil
	b, ok := branches[t.branch]
	if !ok {
		if isRoot {
			return position{}, fmt.Errorf("branch %q for %s does not exist", t.branch, t.source)
		}
		return position{}, nil
	}
	pos := position{exists: true, last: saved, own: true}
	if isRoot {
		pos.last = max(pos.last, im.cfg.StartChange-1)
	}
	if b.Latest == "" {
		return pos, nil
	}
	metadata, err := wc.RevisionMetadata(ctx, b.Latest)
	if err != nil {
		return position{}, err
	}
	source := metadata[t.sourceKey()]
	pos.own = source == t.source || (source == "" && isRoot)
	if pos.own {
		recorded, _ := strconv.Atoi(metadata[lore.ChangelistKey])
		pos.last = max(pos.last, recorded)
	}
	return pos, nil
}

// TargetStatus is one target's progress, for reports.
type TargetStatus struct {
	Name    string // stream or depot path
	Branch  string
	Parent  string // parent branch
	Exists  bool
	Last    int
	Pending []p4.Change
}

// Status reports each target's progress and pending changelists.
func (im *Importer) Status(ctx context.Context) ([]TargetStatus, error) {
	if err := im.begin(ctx); err != nil {
		return nil, err
	}
	targets, err := im.targets(ctx)
	if err != nil {
		return nil, err
	}
	var out []TargetStatus
	for _, t := range targets {
		pos, err := im.position(ctx, t)
		if err != nil {
			return nil, err
		}
		pending, err := t.p4.Changes(ctx, t.path, pos.last)
		if err != nil {
			return nil, err
		}
		ts := TargetStatus{Name: t.String(), Branch: t.branch, Exists: pos.exists, Last: pos.last, Pending: pending}
		if t.parent != nil {
			ts.Parent = t.parent.branch
		}
		out = append(out, ts)
	}
	return out, nil
}

// DryRun writes each target's plan, each pending changelist with its author, then every author still to map.
func (im *Importer) DryRun(ctx context.Context, w io.Writer) error {
	statuses, err := im.Status(ctx)
	if err != nil {
		return err
	}
	authors, err := LoadAuthors(im.cfg.AuthorsFile)
	if err != nil {
		return err
	}
	users := im.loadUsers(ctx)
	missing := map[string]int{}
	total := 0
	for _, ts := range statuses {
		if ts.Branch != "" {
			state := "new branch from " + ts.Parent
			if ts.Exists {
				state = fmt.Sprintf("last imported %d", ts.Last)
			}
			fmt.Fprintf(w, "%s -> %s (%s), %d pending\n", ts.Name, ts.Branch, state, len(ts.Pending))
		}
		for _, ch := range ts.Pending {
			credit, err := im.cfg.ResolveAuthor(ch, authors, users)
			var miss *MissingAuthorError
			switch {
			case errors.As(err, &miss):
				missing[ch.User]++
				fmt.Fprintf(w, "  %d  %-16s  (unmapped)\n", ch.Number, ch.User)
			case err != nil:
				return err
			default:
				fmt.Fprintf(w, "  %d  %-16s  %s -> %s\n", ch.Number, ch.User, credit.Source, credit.User)
			}
		}
		total += len(ts.Pending)
	}
	fmt.Fprintf(w, "\n%d changelist(s) pending\n", total)
	if len(missing) > 0 {
		fmt.Fprintln(w, "Unmapped authors file entries:")
		for _, key := range sortedKeys(missing) {
			fmt.Fprintf(w, "  %s (%d)\n", key, missing[key])
		}
	}
	return nil
}

// Run imports pending changelists, parents before children, at most limit when above 0.
// It stops at the first changelist that cannot be credited and returns how many it imported.
func (im *Importer) Run(ctx context.Context, limit int) (int, error) {
	warnings, err := im.Check(ctx)
	if err != nil {
		return 0, err
	}
	for _, w := range warnings {
		log.Printf("warning: %s", w)
	}
	if err := im.begin(ctx); err != nil {
		return 0, err
	}
	if im.cfg.Push {
		if err := im.pushEarlierWork(ctx, im.lore); err != nil {
			return 0, err
		}
	}
	targets, err := im.targets(ctx)
	if err != nil {
		return 0, err
	}
	done := 0
	for _, t := range targets {
		if limit > 0 && done >= limit {
			break
		}
		n, err := im.runTarget(ctx, t, limit-done)
		done += n
		if errors.Is(err, errWaiting) {
			log.Printf("%s: %v", t, err)
			continue
		}
		if err != nil {
			return done, err
		}
	}
	return done, nil
}

// begin loads the state file and the Perforce accounts for a run.
func (im *Importer) begin(ctx context.Context) error {
	s, err := loadState(im.cfg.StateFile)
	if err != nil {
		return fmt.Errorf("%s: %w", im.cfg.StateFile, err)
	}
	im.state = s
	im.users = nil
	return nil
}

// runTarget imports one target's pending changelists, pushing what it committed before returning.
func (im *Importer) runTarget(ctx context.Context, t *target, limit int) (done int, err error) {
	pos, err := im.position(ctx, t)
	if err != nil {
		return 0, err
	}
	pending, err := t.p4.Changes(ctx, t.path, pos.last)
	if err != nil || len(pending) == 0 {
		return 0, err
	}
	adopt, err := im.enter(ctx, t, pos, pending[0].Number)
	if err != nil {
		return 0, err
	}
	if im.cfg.Push {
		defer func() {
			if im.unpushed == 0 {
				return
			}
			if pushErr := im.push(ctx, t.lore); pushErr != nil && err == nil {
				err = pushErr
			}
		}()
	}
	log.Printf("%s: %d changelist(s) pending, %d to %d", t, len(pending), pending[0].Number, pending[len(pending)-1].Number)
	if im.users == nil {
		im.users = im.loadUsers(ctx)
	}
	for i, ch := range pending {
		if limit > 0 && done >= limit {
			break
		}
		if err := ctx.Err(); err != nil {
			return done, err
		}
		// Re-read so an entry added while halted is picked up.
		authors, err := LoadAuthors(im.cfg.AuthorsFile)
		if err != nil {
			return done, err
		}
		credit, err := im.cfg.ResolveAuthor(ch, authors, im.users)
		if err != nil {
			return done, err
		}
		log.Printf("%d (%d of %d) by %s: %s", ch.Number, i+1, len(pending), credit.User, firstLine(ch.Description))
		if err := im.importChange(ctx, t, ch, credit, adopt && i == 0); err != nil {
			return done, err
		}
		done++
	}
	return done, nil
}

// enter puts the target's working copy and workspace on its branch and stream.
// It returns true when the files on disk have to be matched to the first changelist rather than synced.
func (im *Importer) enter(ctx context.Context, t *target, pos position, first int) (bool, error) {
	if t.source == "" {
		return false, nil
	}
	layout := im.cfg.layout()
	moved := false
	if !t.lore.IsWorkingCopy() {
		if layout.LoreURL == "" {
			return false, fmt.Errorf("%s: no working copy at %s and no loreUrl to clone", t, t.root)
		}
		if err := step(0, "clone into "+t.root, func() error { return t.lore.Clone(ctx, layout.LoreURL) }); err != nil {
			return false, err
		}
		moved = true
	}
	if t.p4.Workspace != im.cfg.P4Client {
		exists, err := t.p4.WorkspaceExists(ctx)
		if err != nil {
			return false, err
		}
		if !exists {
			log.Printf("%s: creating workspace %s", t, t.p4.Workspace)
			create := func() error { return t.p4.CreateStreamWorkspace(ctx, t.source, t.root) }
			if t.classic {
				create = func() error { return t.p4.CreateWorkspace(ctx, t.path, t.root) }
			}
			if err := create(); err != nil {
				return false, err
			}
			moved = true
		}
	}
	spec, err := t.p4.Spec(ctx)
	if err != nil {
		return false, err
	}
	if !samePath(spec.Root, t.root) {
		return false, fmt.Errorf("workspace %q root is %q, not %q", spec.Name, spec.Root, t.root)
	}
	switch {
	case t.classic && !spec.Maps(t.path):
		log.Printf("%s: pointing workspace %s at the path", t, spec.Name)
		if err := t.p4.SetView(ctx, t.path); err != nil {
			return false, err
		}
		moved = true
	case !t.classic && spec.Stream != t.source:
		log.Printf("%s: switching workspace %s to the stream", t, spec.Name)
		if err := t.p4.SwitchStream(ctx, t.source); err != nil {
			return false, err
		}
		moved = true
	}

	if !pos.exists {
		if err := im.createBranch(ctx, t, first); err != nil {
			return false, err
		}
		return true, nil
	}
	current, err := t.lore.Branches(ctx)
	if err != nil {
		return false, err
	}
	if !current[t.branch].Current {
		if err := im.pushPending(ctx, t.lore); err != nil {
			return false, err
		}
		if err := step(0, "switch to branch "+t.branch, func() error { return t.lore.SwitchBranch(ctx, t.branch) }); err != nil {
			return false, err
		}
		moved = true
	}
	if !pos.own {
		return true, nil
	}
	if moved {
		// The files on disk are the branch's newest revision, which is the stream at the last imported changelist.
		if err := step(0, fmt.Sprintf("record %s@%d as synced", t.path, pos.last), func() error {
			return t.p4.Flush(ctx, t.path, pos.last)
		}); err != nil {
			return false, err
		}
	}
	return false, nil
}

// createBranch branches the target from its parent at the parent's last changelist before first.
func (im *Importer) createBranch(ctx context.Context, t *target, first int) error {
	parent := t.parent
	if parent == nil {
		return fmt.Errorf("%s has no parent stream to branch from", t)
	}
	parentPos, err := im.position(ctx, parent)
	if err != nil {
		return err
	}
	if !parentPos.exists {
		return errWaiting
	}
	candidates, err := im.p4.ChangesUpTo(ctx, parent.path, first-1, branchPointSearch)
	if err != nil {
		return err
	}
	if len(candidates) == 0 {
		return fmt.Errorf("%s: %s has no changelists before %d to branch from", t, parent, first)
	}
	if parentPos.last < candidates[0] {
		return errWaiting
	}
	var revision string
	var at int
	for _, n := range candidates {
		if revision, err = im.workingCopy(parent).FindChangelist(ctx, parent.branch, n); err != nil {
			return err
		}
		if revision != "" {
			at = n
			break
		}
	}
	if revision == "" {
		return fmt.Errorf("%s: no revision on %s records a changelist from %d to %d", t, parent.branch, candidates[len(candidates)-1], candidates[0])
	}
	if err := im.pushPending(ctx, t.lore); err != nil {
		return err
	}
	if err := step(0, fmt.Sprintf("sync to %s at changelist %d", parent.branch, at), func() error { return t.lore.SyncTo(ctx, revision) }); err != nil {
		return err
	}
	if err := step(0, "create branch "+t.branch, func() error { return t.lore.CreateBranch(ctx, t.branch) }); err != nil {
		// Return to the parent's newest revision so the working copy matches its branch again.
		if back := t.lore.SwitchBranch(ctx, parent.branch); back != nil {
			log.Printf("warning: returning to %s: %v", parent.branch, back)
		}
		return err
	}
	return nil
}

// adopt makes the files on disk match changelist n without downloading files that already match.
func (im *Importer) adopt(ctx context.Context, t *target, n int) error {
	if err := t.p4.Flush(ctx, t.path, n); err != nil {
		return err
	}
	differing, err := t.p4.Differing(ctx, t.path)
	if err != nil {
		return err
	}
	if err := t.p4.ForceSync(ctx, differing, n); err != nil {
		return err
	}
	have, err := t.p4.Have(ctx, t.path)
	if err != nil {
		return err
	}
	if len(have) == 0 {
		return fmt.Errorf("%s has no files at changelist %d", t, n)
	}
	removed, err := removeUntracked(t.root, have)
	if err != nil {
		return err
	}
	log.Printf("%d: %d file(s) rewritten, %d removed", n, len(differing), removed)
	return nil
}

// importChange brings the workspace to one changelist, stages the result and commits it.
func (im *Importer) importChange(ctx context.Context, t *target, ch p4.Change, credit Credit, adopt bool) error {
	if adopt {
		if err := step(ch.Number, "match files", func() error { return im.adopt(ctx, t, ch.Number) }); err != nil {
			return err
		}
	} else if err := step(ch.Number, "sync", func() error { return t.p4.Sync(ctx, t.path, ch.Number) }); err != nil {
		return err
	}
	if im.cfg.pruneEmptyDirs() {
		removed, err := pruneEmptyDirs(t.root)
		if err != nil {
			return fmt.Errorf("removing empty directories: %w", err)
		}
		if removed > 0 {
			log.Printf("%d: removed %d empty director(ies)", ch.Number, removed)
		}
	}
	var staged int
	if err := step(ch.Number, "stage", func() (err error) { staged, err = t.lore.Stage(ctx); return }); err != nil {
		return err
	}
	if staged == 0 {
		// Entries left staged by a failed commit belong to this changelist.
		status, err := t.lore.Status(ctx)
		if err != nil {
			return err
		}
		if staged = status.Staged; staged > 0 {
			log.Printf("%d: %d entr(ies) already staged by an earlier attempt", ch.Number, staged)
		}
	}
	if staged == 0 {
		log.Printf("%d: no changes in the workspace, skipped", ch.Number)
		return im.saveProgress(t, ch.Number)
	}
	message, metadata, err := im.revision(ctx, t, ch, credit)
	if err != nil {
		return err
	}
	if err := step(ch.Number, fmt.Sprintf("commit %d entr(ies)", staged), func() error {
		return t.lore.Commit(ctx, ch.Number, message, metadata)
	}); err != nil {
		return err
	}
	if err := im.saveProgress(t, ch.Number); err != nil {
		return err
	}
	log.Printf("%d: committed as %s (%s)", ch.Number, credit.User, credit.Source)
	if im.cfg.Push {
		im.unpushed++
		if im.unpushed >= im.cfg.PushEvery {
			return im.push(ctx, t.lore)
		}
	}
	return nil
}

func (im *Importer) saveProgress(t *target, change int) error {
	im.state.set(t.source, change)
	return saveState(im.cfg.StateFile, im.state)
}

// revision builds the message and metadata recorded for a changelist.
func (im *Importer) revision(ctx context.Context, t *target, ch p4.Change, credit Credit) (string, []lore.Field, error) {
	message, tags := im.cfg.Message(ch.Number, ch.Description)
	metadata := []lore.Field{
		{Key: "created-by", Value: credit.User},
		{Key: "p4-user", Value: ch.User},
		{Key: "p4-workspace", Value: ch.Workspace},
		{Key: "p4-time", Value: strconv.FormatInt(ch.Time, 10)},
		{Key: t.sourceKey(), Value: t.source},
	}
	if im.cfg.recordJobs() {
		jobs, err := t.p4.Jobs(ctx, ch.Number)
		if err != nil {
			return "", nil, err
		}
		if len(jobs) > 0 {
			metadata = append(metadata, lore.Field{Key: "p4-jobs", Value: strings.Join(jobs, ", ")})
		}
	}
	metadata = append(metadata, tags...)
	kept := metadata[:0]
	for _, f := range metadata {
		if f.Value != "" {
			kept = append(kept, f)
		}
	}
	return message, kept, nil
}

// workingCopy returns the target's own working copy, or the main one while the target has none yet.
func (im *Importer) workingCopy(t *target) lore.Client {
	if t.lore.IsWorkingCopy() {
		return t.lore
	}
	return im.lore
}

// pushEarlierWork pushes revisions a previous run committed but did not push.
func (im *Importer) pushEarlierWork(ctx context.Context, wc lore.Client) error {
	status, err := wc.Status(ctx)
	if err != nil {
		return err
	}
	if !status.Ahead {
		return nil
	}
	log.Printf("pushing revisions from an earlier run")
	return im.push(ctx, wc)
}

// pushPending pushes unpushed commits before the working copy leaves its branch.
func (im *Importer) pushPending(ctx context.Context, wc lore.Client) error {
	if !im.cfg.Push || im.unpushed == 0 {
		return nil
	}
	return im.push(ctx, wc)
}

func (im *Importer) push(ctx context.Context, wc lore.Client) error {
	if err := step(0, "push", func() error { return wc.Push(ctx) }); err != nil {
		return err
	}
	im.unpushed = 0
	return nil
}

// loadUsers returns the Perforce accounts for email and full-name matching; empty when they cannot be listed.
func (im *Importer) loadUsers(ctx context.Context) map[string]p4.User {
	users, err := im.p4.Users(ctx)
	if err != nil {
		log.Printf("warning: matching by email and full name is off: %v", err)
		return map[string]p4.User{}
	}
	return users
}

// step logs an import step as it starts and how long it took.
func step(change int, what string, run func() error) error {
	prefix := ""
	if change > 0 {
		prefix = strconv.Itoa(change) + ": "
	}
	log.Printf("%s%s", prefix, what)
	start := time.Now()
	err := run()
	log.Printf("%s%s done in %s", prefix, what, time.Since(start).Round(time.Second))
	return err
}

func sortedKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(line)
}

func samePath(a, b string) bool {
	a, _ = filepath.Abs(filepath.Clean(a))
	b, _ = filepath.Abs(filepath.Clean(b))
	return strings.EqualFold(a, b)
}
