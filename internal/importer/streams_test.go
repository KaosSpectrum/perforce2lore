package importer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KaosSpectrum/perforce2lore/internal/lore"
	"github.com/KaosSpectrum/perforce2lore/internal/p4"
)

var testStreams = []p4.Stream{
	{Path: "//Game/Main", Parent: "none", Type: "mainline"},
	{Path: "//Game/Tools", Parent: "none", Type: "mainline"},
	{Path: "//Game/Dev", Parent: "//Game/Main", Type: "development"},
	{Path: "//Game/Release-Dev", Parent: "//Game/Main", Type: "development"},
	{Path: "//Game/Release-1.0", Parent: "//Game/Release-Dev", Type: "release"},
	{Path: "//Game/Release-0.9", Parent: "//Game/Release-1.0", Type: "release"},
	{Path: "//Game/Main-Virtual", Parent: "//Game/Main", Type: "virtual"},
}

func streamsConfig(t *testing.T, streams string) *Config {
	return testConfig(t, `{"loreRoot":"/srv/import/main","p4Client":"import","streams":`+streams+`}`)
}

func plan(t *testing.T, cfg *Config) map[string]*target {
	t.Helper()
	targets, err := planStreams(cfg, testStreams, p4.Client{Workspace: "import"}, lore.Client{Root: cfg.LoreRoot})
	if err != nil {
		t.Fatal(err)
	}
	byStream := map[string]*target{}
	for i, tg := range targets {
		if i > 0 && tg.parent != nil && byStream[tg.parent.source] == nil {
			t.Fatalf("%s is ordered before its parent", tg.source)
		}
		byStream[tg.source] = tg
	}
	return byStream
}

func TestRootOnly(t *testing.T) {
	got := plan(t, streamsConfig(t, `{"root":"//Game/Main"}`))
	if len(got) != 1 || got["//Game/Main"].branch != "main" || got["//Game/Main"].path != "//Game/Main/..." {
		t.Fatalf("got %v", got)
	}
}

func TestIncludeAndExclude(t *testing.T) {
	got := plan(t, streamsConfig(t, `{"root":"//Game/Main","include":["//Game/Release-*"],"exclude":["//Game/Release-0.*"]}`))
	if len(got) != 3 || got["//Game/Release-0.9"] != nil || got["//Game/Dev"] != nil {
		t.Fatalf("got %v", got)
	}
	if got["//Game/Release-1.0"].parent.source != "//Game/Release-Dev" || got["//Game/Release-1.0"].branch != "release-1.0" {
		t.Fatal("Release-1.0 should branch from Release-Dev")
	}
}

func TestParentSkipsStreamsNotImported(t *testing.T) {
	got := plan(t, streamsConfig(t, `{"root":"//Game/Main","branches":{"//Game/Release-1.0":"rel"}}`))
	if got["//Game/Release-1.0"].parent.source != "//Game/Main" || got["//Game/Release-1.0"].branch != "rel" {
		t.Fatal("Release-1.0 should branch from Main when Release-Dev is not imported")
	}
}

func TestVirtualStreamsAreNotDiscovered(t *testing.T) {
	got := plan(t, streamsConfig(t, `{"root":"//Game/Main","include":["//Game/*"]}`))
	if got["//Game/Main-Virtual"] != nil || got["//Game/Tools"] != nil || len(got) != 5 {
		t.Fatalf("got %d targets", len(got))
	}
}

func TestSeparateLayoutGivesEachStreamItsOwnWorkspace(t *testing.T) {
	cfg := testConfig(t, `{"loreRoot":"/srv/import/main","p4Client":"import","push":true,
		"streams":{"root":"//Game/Main","include":["//Game/Dev"],"layout":"separate"}}`)
	got := plan(t, cfg)
	dev := got["//Game/Dev"]
	if dev.p4.Workspace != "import-dev" || !strings.HasSuffix(dev.root, "dev") || dev.lore.Root != dev.root {
		t.Fatalf("got %+v", dev)
	}
	if got["//Game/Main"].p4.Workspace != "import" {
		t.Fatal("the root stream should use the main workspace")
	}
}

func TestStreamSelectionErrors(t *testing.T) {
	for _, streams := range []string{
		`{"root":"//Game/Missing"}`,
		`{"root":"//Game/Main","branches":{"//Game/Tools":"tools"}}`,
		`{"root":"//Game/Main","branches":{"//Game/Main-Virtual":"v"}}`,
		`{"root":"//Game/Main","branches":{"//Game/Dev":"main"}}`,
	} {
		cfg := streamsConfig(t, streams)
		if _, err := planStreams(cfg, testStreams, p4.Client{}, lore.Client{}); err == nil {
			t.Errorf("%s: expected an error", streams)
		}
	}
}

func TestSeparateLayoutNeedsPush(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.json")
	os.WriteFile(path, []byte(`{"loreRoot":"/w","p4Client":"import","streams":{"root":"//Game/Main","layout":"separate"}}`), 0o644)
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("expected an error")
	}
}

func TestClassicPathsLinkToTheirParents(t *testing.T) {
	cfg := testConfig(t, `{"loreRoot":"/w","p4Client":"import","paths":{
		"root":{"path":"//depot/game/main/..."},
		"branches":[
			{"path":"//depot/game/rel-1.1","parent":"//depot/game/rel-1.0"},
			{"path":"//depot/game/rel-1.0","branch":"release"}
		]}}`)
	targets, err := planPaths(cfg, p4.Client{}, lore.Client{})
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 3 || targets[0].branch != "main" || targets[1].branch != "release" || targets[2].branch != "rel-1.1" {
		t.Fatalf("unexpected order or names: %v, %v, %v", targets[0], targets[1], targets[2])
	}
	if targets[1].parent != targets[0] || targets[2].parent != targets[1] || !targets[2].classic || targets[2].sourceKey() != PathKey {
		t.Fatal("parents not linked")
	}
}

func TestClassicPathErrors(t *testing.T) {
	for _, paths := range []string{
		`{"root":{"path":"depot/main"}}`,
		`{"root":{"path":"//d/main"},"branches":[{"path":"//d/a","parent":"//d/missing"}]}`,
		`{"root":{"path":"//d/main"},"branches":[{"path":"//d/a"},{"path":"//d/a"}]}`,
	} {
		path := filepath.Join(t.TempDir(), "c.json")
		os.WriteFile(path, []byte(`{"loreRoot":"/w","p4Client":"import","paths":`+paths+`}`), 0o644)
		if _, err := LoadConfig(path); err == nil {
			t.Errorf("%s: expected an error", paths)
		}
	}
}
