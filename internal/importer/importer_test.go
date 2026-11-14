package importer

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/KaosSpectrum/perforce2lore/internal/lore"
	"github.com/KaosSpectrum/perforce2lore/internal/p4"
)

const taggedDescription = "Update the lighting\n\n#review alex\n#author Sam Lee\n#build -1"

func testConfig(t *testing.T, json string) *Config {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "perforce2lore.json")
	if err := os.WriteFile(path, []byte(json), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func authorConfig(t *testing.T) *Config {
	return testConfig(t, `{"depotPath":"//Game/main","loreRoot":"/w"}`)
}

func TestConfigDefaults(t *testing.T) {
	c := testConfig(t, `{"depotPath":"//Game/main/","loreRoot":"/w"}`)
	if c.DepotPath != "//Game/main/..." || c.PushEvery != 1 || c.PollSeconds != 60 || c.DescriptionTags != TagsKeep {
		t.Fatalf("unexpected defaults: %+v", c)
	}
	if !c.recordJobs() || !c.pruneEmptyDirs() {
		t.Fatal("jobs and pruning should be on by default")
	}
	if filepath.Base(c.StateFile) != "perforce2lore.state.json" || filepath.Base(c.AuthorsFile) != "authors.json" {
		t.Fatalf("unexpected files: %s, %s", c.StateFile, c.AuthorsFile)
	}
}

func TestConfigRejectsBadSettings(t *testing.T) {
	for _, json := range []string{
		`{"loreRoot":"/w"}`,
		`{"depotPath":"//a","loreRoot":"/w","descriptionTags":"drop"}`,
		`{"depotPath":"//a","loreRoot":"/w","stripLines":["("]}`,
	} {
		path := filepath.Join(t.TempDir(), "c.json")
		os.WriteFile(path, []byte(json), 0o644)
		if _, err := LoadConfig(path); err == nil {
			t.Errorf("%s: expected an error", json)
		}
	}
}

func TestUserIsCreditedByName(t *testing.T) {
	c := authorConfig(t)
	got, err := c.ResolveAuthor(p4.Change{Number: 1, User: "jsmith"}, Authors{"jsmith": "john"}, nil)
	if err != nil || got.User != "john" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestUserIsCreditedByEmailOrFullName(t *testing.T) {
	c := authorConfig(t)
	users := map[string]p4.User{"jsmith": {Name: "jsmith", Email: "john@example.com", FullName: "John Smith"}}
	got, err := c.ResolveAuthor(p4.Change{Number: 1, User: "jsmith"}, Authors{"john@example.com": "john"}, users)
	if err != nil || got.User != "john" || got.Source != "john@example.com" {
		t.Fatalf("by email: got %+v, %v", got, err)
	}
	got, err = c.ResolveAuthor(p4.Change{Number: 1, User: "jsmith"}, Authors{"john smith": "john"}, users)
	if err != nil || got.User != "john" {
		t.Fatalf("by full name: got %+v, %v", got, err)
	}
}

func TestChangelistEntryWins(t *testing.T) {
	c := authorConfig(t)
	got, err := c.ResolveAuthor(p4.Change{Number: 8, User: "jsmith"}, Authors{"@8": "alex", "jsmith": "john"}, nil)
	if err != nil || got.User != "alex" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestUnmappedAuthorHalts(t *testing.T) {
	c := authorConfig(t)
	_, err := c.ResolveAuthor(p4.Change{Number: 7, User: "build"}, Authors{"jsmith": "john"}, nil)
	var miss *MissingAuthorError
	if !errors.As(err, &miss) || miss.User != "build" || miss.Change != 7 {
		t.Fatalf("got %v", err)
	}
}

func TestDefaultAuthorReplacesHalt(t *testing.T) {
	c := testConfig(t, `{"depotPath":"//a","loreRoot":"/w","defaultAuthor":"importer"}`)
	got, err := c.ResolveAuthor(p4.Change{Number: 1, User: "nobody"}, Authors{}, nil)
	if err != nil || got.User != "importer" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestLoadAuthorsIgnoresCaseAndBlanks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "authors.json")
	os.WriteFile(path, []byte(`{"Sam Lee":"sam","todo":""}`), 0o644)
	a, err := LoadAuthors(path)
	if err != nil || a["sam lee"] != "sam" {
		t.Fatalf("got %v, %v", a, err)
	}
	if _, ok := a["todo"]; ok {
		t.Fatal("a blank entry should be unmapped")
	}
}

func TestMessageKeepsTagsByDefault(t *testing.T) {
	c := testConfig(t, `{"depotPath":"//a","loreRoot":"/w"}`)
	msg, tags := c.Message(5, taggedDescription)
	if msg != taggedDescription || tags != nil {
		t.Fatalf("got %q, %v", msg, tags)
	}
}

func TestMessageMovesTagsToMetadata(t *testing.T) {
	c := testConfig(t, `{"depotPath":"//a","loreRoot":"/w","descriptionTags":"move"}`)
	msg, tags := c.Message(5, taggedDescription+"\n#review sam")
	want := []lore.Field{
		{Key: "p4-tag.review", Value: "alex, sam"},
		{Key: "p4-tag.author", Value: "Sam Lee"},
		{Key: "p4-tag.build", Value: "-1"},
	}
	if msg != "Update the lighting" || !reflect.DeepEqual(tags, want) {
		t.Fatalf("got %q, %v", msg, tags)
	}
}

func TestMessageCopiesTagsToMetadata(t *testing.T) {
	c := testConfig(t, `{"depotPath":"//a","loreRoot":"/w","descriptionTags":"metadata","tagPrefix":"tag."}`)
	msg, tags := c.Message(5, "Fix\n#urgent")
	if msg != "Fix\n#urgent" || len(tags) != 1 || tags[0] != (lore.Field{Key: "tag.urgent", Value: "true"}) {
		t.Fatalf("got %q, %v", msg, tags)
	}
}

func TestTagMapRenamesMergesAndDrops(t *testing.T) {
	c := testConfig(t, `{"depotPath":"//a","loreRoot":"/w","descriptionTags":"move",
		"tagMap":{"#Review":"review.by","rb":"review.by","author":""}}`)
	msg, tags := c.Message(5, taggedDescription+"\n#rb sam")
	want := []lore.Field{
		{Key: "review.by", Value: "alex, sam"},
		{Key: "p4-tag.build", Value: "-1"},
	}
	if msg != "Update the lighting" || !reflect.DeepEqual(tags, want) {
		t.Fatalf("got %q, %v", msg, tags)
	}
}

func TestOnlyMappedTagsKeepsOthersInMessage(t *testing.T) {
	c := testConfig(t, `{"depotPath":"//a","loreRoot":"/w","descriptionTags":"move",
		"tagMap":{"review":"review.by"},"onlyMappedTags":true}`)
	msg, tags := c.Message(5, taggedDescription)
	want := []lore.Field{{Key: "review.by", Value: "alex"}}
	if msg != "Update the lighting\n\n#author Sam Lee\n#build -1" || !reflect.DeepEqual(tags, want) {
		t.Fatalf("got %q, %v", msg, tags)
	}
}

func TestMessageStripsLinesAndFallsBack(t *testing.T) {
	c := testConfig(t, `{"depotPath":"//a","loreRoot":"/w","stripLines":["^\\s*\\[skip-ci\\]"]}`)
	if msg, _ := c.Message(5, "Fix crash\n\n\n[skip-ci]\nDetails"); msg != "Fix crash\n\nDetails" {
		t.Fatalf("got %q", msg)
	}
	if msg, _ := c.Message(42, " [skip-ci] "); msg != "Changelist 42" {
		t.Fatalf("got %q", msg)
	}
}

func TestPruneEmptyDirsKeepsFilesAndLore(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"a/b/c", "keep/sub", ".lore/empty", "x"} {
		os.MkdirAll(filepath.Join(root, d), 0o755)
	}
	os.WriteFile(filepath.Join(root, "keep", "sub", "f.txt"), []byte("x"), 0o644)
	n, err := pruneEmptyDirs(root)
	if err != nil || n != 4 {
		t.Fatalf("removed %d, %v", n, err)
	}
	for _, d := range []string{"keep/sub", ".lore/empty"} {
		if _, err := os.Stat(filepath.Join(root, d)); err != nil {
			t.Errorf("%s should remain", d)
		}
	}
	for _, d := range []string{"a", "x"} {
		if _, err := os.Stat(filepath.Join(root, d)); err == nil {
			t.Errorf("%s should be removed", d)
		}
	}
}

func TestStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := loadState(path)
	if err != nil || s.last("") != 0 {
		t.Fatalf("missing file: %+v, %v", s, err)
	}
	s.set("", 1234)
	s.set("//Game/Dev", 1300)
	if err := saveState(path, s); err != nil {
		t.Fatal(err)
	}
	s, err = loadState(path)
	if err != nil || s.last("") != 1234 || s.last("//Game/Dev") != 1300 || s.last("//Game/Other") != 0 {
		t.Fatalf("got %+v, %v", s, err)
	}
}

func TestOldStateFileStillReads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	os.WriteFile(path, []byte(`{"lastChange":2500}`), 0o644)
	if s, err := loadState(path); err != nil || s.last("") != 2500 {
		t.Fatalf("got %+v, %v", s, err)
	}
}

func TestRemoveUntrackedKeepsSyncedFilesAndLore(t *testing.T) {
	root := t.TempDir()
	for _, f := range []string{"keep/a.txt", "drop/b.txt", "keep/c.txt", ".lore/data"} {
		os.MkdirAll(filepath.Dir(filepath.Join(root, f)), 0o755)
		os.WriteFile(filepath.Join(root, f), []byte("x"), 0o644)
	}
	n, err := removeUntracked(root, []string{filepath.Join(root, "keep", "a.txt")})
	if err != nil || n != 2 {
		t.Fatalf("removed %d, %v", n, err)
	}
	for f, want := range map[string]bool{"keep/a.txt": true, "keep/c.txt": false, "drop": false, ".lore/data": true} {
		_, err := os.Stat(filepath.Join(root, f))
		if (err == nil) != want {
			t.Errorf("%s: exists=%v, want %v", f, err == nil, want)
		}
	}
}

func TestLockIsExclusive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.lock")
	first, err := acquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquireLock(path); err == nil {
		t.Fatal("a second lock should fail")
	}
	first.Close()
	again, err := acquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	again.Close()
}
