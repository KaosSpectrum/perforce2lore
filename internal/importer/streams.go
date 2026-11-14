package importer

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/KaosSpectrum/perforce2lore/internal/lore"
	"github.com/KaosSpectrum/perforce2lore/internal/p4"
)

// Workspace layouts for importing several branches.
const (
	LayoutShared   = "shared"   // one working copy and workspace that switch between branches
	LayoutSeparate = "separate" // a working copy and workspace for each branch
)

// Metadata keys naming where a revision was imported from.
const (
	StreamKey = "p4-stream"
	PathKey   = "p4-path"
)

// LayoutConfig sets where each imported branch's files live.
type LayoutConfig struct {
	// Layout is shared or separate.
	Layout string `json:"layout"`
	// WorkspaceName and WorkspaceRoot name each branch's workspace and folder in the separate layout.
	// {branch} and {stream} are replaced by the branch name and the last segment of the stream or path.
	WorkspaceName string `json:"workspaceName"`
	WorkspaceRoot string `json:"workspaceRoot"`
	// LoreURL is the repository cloned into a new working copy in the separate layout.
	LoreURL string `json:"loreUrl"`
}

// StreamsConfig selects the streams to import.
type StreamsConfig struct {
	// Root is the top stream, imported into RootBranch; every other stream must derive from it.
	Root       string `json:"root"`
	RootBranch string `json:"rootBranch"`
	// Branches names the Lore branch of a stream and selects it for import.
	Branches map[string]string `json:"branches"`
	// Include selects derived streams whose path matches a pattern, such as //Game/Release-*.
	Include []string `json:"include"`
	// Exclude leaves out included streams whose path matches a pattern.
	Exclude []string `json:"exclude"`
	LayoutConfig
}

// PathsConfig selects the classic depot paths to import, each with the path it was branched from.
type PathsConfig struct {
	// Root is imported into the working copy's existing branch.
	Root PathEntry `json:"root"`
	// Branches are imported into new branches, created from their parent.
	Branches []PathEntry `json:"branches"`
	LayoutConfig
}

// PathEntry is one classic depot path and its Lore branch.
type PathEntry struct {
	Path   string `json:"path"`
	Branch string `json:"branch"`
	// Parent is the path it was branched from; empty means the root path.
	Parent string `json:"parent"`
}

func (s *StreamsConfig) prepare(c *Config) error {
	s.Root = trimDepotPath(s.Root)
	if !strings.HasPrefix(s.Root, "//") {
		return fmt.Errorf("root must be a stream such as //Game/Main")
	}
	if s.RootBranch == "" {
		s.RootBranch = "main"
	}
	for _, pattern := range append(append([]string{}, s.Include...), s.Exclude...) {
		if _, err := path.Match(pattern, ""); err != nil {
			return fmt.Errorf("pattern %q: %w", pattern, err)
		}
	}
	return s.LayoutConfig.prepare(c)
}

func (p *PathsConfig) prepare(c *Config) error {
	p.Root.Path = trimDepotPath(p.Root.Path)
	if !strings.HasPrefix(p.Root.Path, "//") {
		return fmt.Errorf("root.path must be a depot path such as //depot/game/main")
	}
	if p.Root.Branch == "" {
		p.Root.Branch = "main"
	}
	known := map[string]bool{p.Root.Path: true}
	for i := range p.Branches {
		b := &p.Branches[i]
		b.Path, b.Parent = trimDepotPath(b.Path), trimDepotPath(b.Parent)
		if !strings.HasPrefix(b.Path, "//") {
			return fmt.Errorf("branches[%d].path must be a depot path", i)
		}
		if known[b.Path] {
			return fmt.Errorf("%s is listed twice", b.Path)
		}
		known[b.Path] = true
	}
	for _, b := range p.Branches {
		if b.Parent != "" && !known[b.Parent] {
			return fmt.Errorf("%s: parent %s is not imported", b.Path, b.Parent)
		}
	}
	return p.LayoutConfig.prepare(c)
}

func (l *LayoutConfig) prepare(c *Config) error {
	if c.P4Client == "" {
		return fmt.Errorf("p4Client is required")
	}
	switch l.Layout = strings.ToLower(l.Layout); l.Layout {
	case "":
		l.Layout = LayoutShared
	case LayoutShared:
	case LayoutSeparate:
		if !c.Push {
			return fmt.Errorf("the separate layout needs push, so each working copy can read the others' revisions")
		}
		if l.WorkspaceName == "" {
			l.WorkspaceName = c.P4Client + "-{branch}"
		}
		if l.WorkspaceRoot == "" {
			l.WorkspaceRoot = filepath.Join(filepath.Dir(c.LoreRoot), "{branch}")
		}
		if !strings.Contains(l.WorkspaceName, "{") || !strings.Contains(l.WorkspaceRoot, "{") {
			return fmt.Errorf("workspaceName and workspaceRoot need {branch} or {stream} so each branch gets its own")
		}
	default:
		return fmt.Errorf("layout must be %q or %q", LayoutShared, LayoutSeparate)
	}
	return nil
}

// layout returns the branch layout settings, or nil when importing a single depot path.
func (c *Config) layout() *LayoutConfig {
	switch {
	case c.Streams != nil:
		return &c.Streams.LayoutConfig
	case c.Paths != nil:
		return &c.Paths.LayoutConfig
	}
	return nil
}

func trimDepotPath(p string) string {
	return strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(p), "/..."), "/")
}

// target is one depot path imported into one Lore branch.
type target struct {
	source  string // stream or classic path; empty when importing a single depot path
	classic bool   // source is a classic path rather than a stream
	branch  string // empty when importing a single depot path
	parent  *target
	path    string // depot path with /...
	root    string // working copy and workspace root
	p4      p4.Client
	lore    lore.Client
}

func (t *target) String() string {
	if t.source == "" {
		return t.path
	}
	return t.source
}

// sourceKey is the metadata key that records the target's stream or path.
func (t *target) sourceKey() string {
	if t.classic {
		return PathKey
	}
	return StreamKey
}

// targets returns what to import, parents before children.
func (im *Importer) targets(ctx context.Context) ([]*target, error) {
	cfg := im.cfg
	switch {
	case cfg.Streams != nil:
		all, err := im.p4.Streams(ctx, streamDepot(cfg.Streams.Root)+"/...")
		if err != nil {
			return nil, err
		}
		return planStreams(cfg, all, im.p4, im.lore)
	case cfg.Paths != nil:
		return planPaths(cfg, im.p4, im.lore)
	}
	return []*target{{path: cfg.DepotPath, root: cfg.LoreRoot, p4: im.p4, lore: im.lore}}, nil
}

// streamDepot returns the depot part of a stream path, such as //Game.
func streamDepot(stream string) string {
	rest := strings.TrimPrefix(stream, "//")
	depot, _, _ := strings.Cut(rest, "/")
	return "//" + depot
}

// source is one stream or path chosen for import.
type source struct {
	name   string
	branch string
	parent string // nearest imported ancestor; empty for the root
	depth  int    // steps from the root
}

// planStreams picks the streams to import and links each to its nearest imported ancestor.
func planStreams(cfg *Config, all []p4.Stream, baseP4 p4.Client, baseLore lore.Client) ([]*target, error) {
	sc := cfg.Streams
	byPath := make(map[string]p4.Stream, len(all))
	for _, s := range all {
		byPath[s.Path] = s
	}
	if _, ok := byPath[sc.Root]; !ok {
		return nil, fmt.Errorf("stream %s not found", sc.Root)
	}

	// ancestors returns a stream's parents up to the root, nearest first; false when it does not derive from the root.
	ancestors := func(stream string) ([]string, bool) {
		var chain []string
		for s := byPath[stream]; s.Path != sc.Root; s = byPath[s.Parent] {
			if s.Parent == "" || s.Parent == "none" {
				return nil, false
			}
			if _, ok := byPath[s.Parent]; !ok || len(chain) > len(byPath) {
				return nil, false
			}
			chain = append(chain, s.Parent)
		}
		return chain, true
	}

	selected := map[string]bool{sc.Root: true}
	for stream := range sc.Branches {
		s, ok := byPath[stream]
		if !ok {
			return nil, fmt.Errorf("stream %s not found", stream)
		}
		if s.Type == "virtual" {
			return nil, fmt.Errorf("stream %s is virtual and has no changelists of its own", stream)
		}
		if _, ok := ancestors(stream); !ok && stream != sc.Root {
			return nil, fmt.Errorf("stream %s does not derive from %s", stream, sc.Root)
		}
		selected[stream] = true
	}
	for _, s := range all {
		if selected[s.Path] || s.Type == "virtual" || !matchesAny(sc.Include, s.Path) || matchesAny(sc.Exclude, s.Path) {
			continue
		}
		if _, ok := ancestors(s.Path); ok {
			selected[s.Path] = true
		}
	}

	var sources []source
	for stream := range selected {
		src := source{name: stream, branch: sc.Branches[stream]}
		if stream == sc.Root {
			src.branch = sc.RootBranch
		}
		chain, _ := ancestors(stream)
		src.depth = len(chain)
		for _, a := range chain {
			if selected[a] {
				src.parent = a
				break
			}
		}
		sources = append(sources, src)
	}
	return buildTargets(cfg, &sc.LayoutConfig, sources, false, baseP4, baseLore)
}

// planPaths links the configured classic paths to their parents.
func planPaths(cfg *Config, baseP4 p4.Client, baseLore lore.Client) ([]*target, error) {
	pc := cfg.Paths
	parents := map[string]string{}
	sources := []source{{name: pc.Root.Path, branch: pc.Root.Branch}}
	for _, b := range pc.Branches {
		parent := b.Parent
		if parent == "" {
			parent = pc.Root.Path
		}
		parents[b.Path] = parent
		sources = append(sources, source{name: b.Path, branch: b.Branch, parent: parent})
	}
	for i := range sources {
		for p := sources[i].parent; p != ""; p = parents[p] {
			sources[i].depth++
			if sources[i].depth > len(sources) {
				return nil, fmt.Errorf("%s: parents form a loop", sources[i].name)
			}
		}
	}
	return buildTargets(cfg, &pc.LayoutConfig, sources, true, baseP4, baseLore)
}

// buildTargets creates a target for each source, ordered parents first.
func buildTargets(cfg *Config, layout *LayoutConfig, sources []source, classic bool, baseP4 p4.Client, baseLore lore.Client) ([]*target, error) {
	sort.Slice(sources, func(i, j int) bool {
		if sources[i].depth != sources[j].depth {
			return sources[i].depth < sources[j].depth
		}
		return sources[i].name < sources[j].name
	})
	byName := map[string]*target{}
	branches := map[string]string{}
	targets := make([]*target, 0, len(sources))
	for _, src := range sources {
		branch := src.branch
		if branch == "" {
			branch = defaultBranch(src.name)
		}
		if other, ok := branches[strings.ToLower(branch)]; ok {
			return nil, fmt.Errorf("%s and %s both import into branch %q", other, src.name, branch)
		}
		branches[strings.ToLower(branch)] = src.name
		t := &target{
			source: src.name, classic: classic, branch: branch, parent: byName[src.parent],
			path: depotPath(src.name), root: cfg.LoreRoot, p4: baseP4, lore: baseLore,
		}
		if layout.Layout == LayoutSeparate && src.parent != "" {
			t.root = filepath.Clean(expand(layout.WorkspaceRoot, branch, src.name))
			t.p4.Workspace = expand(layout.WorkspaceName, branch, src.name)
			t.lore.Root = t.root
		}
		byName[src.name] = t
		targets = append(targets, t)
	}
	return targets, nil
}

func matchesAny(patterns []string, stream string) bool {
	for _, p := range patterns {
		if ok, _ := path.Match(p, stream); ok {
			return true
		}
	}
	return false
}

// defaultBranch names a branch after the last segment of its stream or path, in lower case.
func defaultBranch(name string) string {
	return strings.ToLower(strings.ReplaceAll(path.Base(name), " ", "-"))
}

func expand(template, branch, name string) string {
	return strings.NewReplacer("{branch}", branch, "{stream}", path.Base(name)).Replace(template)
}
