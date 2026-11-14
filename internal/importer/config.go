package importer

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Description tag handling modes.
const (
	TagsKeep     = "keep"     // leave tag lines in the message
	TagsMetadata = "metadata" // copy tag lines to metadata and keep them in the message
	TagsMove     = "move"     // move tag lines to metadata
)

// Config is the importer's JSON settings file.
type Config struct {
	// Perforce connection; empty values fall back to the P4 environment or P4CONFIG.
	P4Exe     string `json:"p4Exe"`
	P4Port    string `json:"p4Port"`
	P4User    string `json:"p4User"`
	P4Client  string `json:"p4Client"`
	P4Charset string `json:"p4Charset"`

	// DepotPath is the depot path to import, such as //Game/main/...; not used with Streams or Paths.
	DepotPath string `json:"depotPath"`
	// Streams imports a stream and the streams derived from it, each into its own branch.
	Streams *StreamsConfig `json:"streams"`
	// Paths imports classic depot paths, each into its own branch.
	Paths *PathsConfig `json:"paths"`
	// StartChange is the first changelist imported into an empty repository.
	StartChange int `json:"startChange"`
	// SyncThreads above 1 syncs in parallel.
	SyncThreads int `json:"syncThreads"`

	LoreExe string `json:"loreExe"`
	// LoreRoot is the Lore working copy; it must also be the Perforce workspace root.
	LoreRoot string `json:"loreRoot"`
	// Push sends revisions to the remote, every PushEvery commits and when a run ends.
	Push      bool `json:"push"`
	PushEvery int  `json:"pushEvery"`

	// AuthorsFile maps author names to Lore users; it is re-read before each changelist.
	AuthorsFile string `json:"authorsFile"`
	// UseP4Users credits an unmapped changelist to the Lore user with the same name as its Perforce user.
	UseP4Users bool `json:"useP4Users"`
	// DefaultAuthor is credited when no author is mapped; empty halts the import instead.
	DefaultAuthor string `json:"defaultAuthor"`

	// DescriptionTags handles "#tag value" lines: keep, metadata or move.
	DescriptionTags string `json:"descriptionTags"`
	// TagPrefix starts the metadata key of each description tag.
	TagPrefix string `json:"tagPrefix"`
	// TagMap sets the metadata key of named tags; an empty key drops the tag.
	TagMap map[string]string `json:"tagMap"`
	// OnlyMappedTags leaves tags missing from TagMap in the message.
	OnlyMappedTags bool `json:"onlyMappedTags"`
	// StripLines are regular expressions; matching description lines are left out of the message.
	StripLines []string `json:"stripLines"`
	// RecordJobs stores the jobs a changelist fixes as p4-jobs.
	RecordJobs *bool `json:"recordJobs"`
	// PruneEmptyDirs removes directories left empty after each sync.
	PruneEmptyDirs *bool `json:"pruneEmptyDirs"`

	// PollSeconds is the wait between checks in watch mode.
	PollSeconds int    `json:"pollSeconds"`
	StateFile   string `json:"stateFile"`

	tagMap     map[string]string
	stripLines []*regexp.Regexp
}

// LoadConfig reads a settings file and fills in defaults.
// Relative file paths are resolved against the settings file's directory.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	c := &Config{}
	if err := json.Unmarshal(data, c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := c.prepare(filepath.Dir(path), strings.TrimSuffix(path, filepath.Ext(path))+".state.json"); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

func (c *Config) prepare(dir, defaultState string) error {
	if c.LoreRoot == "" {
		return fmt.Errorf("loreRoot is required")
	}
	c.LoreRoot = filepath.Clean(c.LoreRoot)
	switch {
	case c.Streams != nil && c.Paths != nil:
		return fmt.Errorf("use streams or paths, not both")
	case c.Streams != nil:
		if err := c.Streams.prepare(c); err != nil {
			return fmt.Errorf("streams: %w", err)
		}
	case c.Paths != nil:
		if err := c.Paths.prepare(c); err != nil {
			return fmt.Errorf("paths: %w", err)
		}
	case c.DepotPath == "":
		return fmt.Errorf("depotPath, streams or paths is required")
	default:
		c.DepotPath = depotPath(c.DepotPath)
	}
	if c.PushEvery <= 0 {
		c.PushEvery = 1
	}
	if c.PollSeconds <= 0 {
		c.PollSeconds = 60
	}
	if c.AuthorsFile == "" {
		c.AuthorsFile = "authors.json"
	}
	c.AuthorsFile = resolve(dir, c.AuthorsFile)
	if c.StateFile == "" {
		c.StateFile = defaultState
	}
	c.StateFile = resolve(dir, c.StateFile)

	switch c.DescriptionTags = strings.ToLower(c.DescriptionTags); c.DescriptionTags {
	case "":
		c.DescriptionTags = TagsKeep
	case TagsKeep, TagsMetadata, TagsMove:
	default:
		return fmt.Errorf("descriptionTags must be %q, %q or %q", TagsKeep, TagsMetadata, TagsMove)
	}
	if c.TagPrefix == "" {
		c.TagPrefix = "p4-tag."
	}
	c.tagMap = make(map[string]string, len(c.TagMap))
	for tag, key := range c.TagMap {
		c.tagMap[strings.ToLower(strings.TrimPrefix(strings.TrimSpace(tag), "#"))] = strings.TrimSpace(key)
	}

	for _, s := range c.StripLines {
		re, err := regexp.Compile(s)
		if err != nil {
			return fmt.Errorf("stripLines %q: %w", s, err)
		}
		c.stripLines = append(c.stripLines, re)
	}
	return nil
}

// recordJobs reports whether fixed jobs are stored; on by default.
func (c *Config) recordJobs() bool { return c.RecordJobs == nil || *c.RecordJobs }

// pruneEmptyDirs reports whether empty directories are removed after a sync; on by default.
func (c *Config) pruneEmptyDirs() bool { return c.PruneEmptyDirs == nil || *c.PruneEmptyDirs }

// depotPath adds "/..." to a depot path or stream that lacks it.
func depotPath(path string) string {
	if strings.HasSuffix(path, "/...") {
		return path
	}
	return strings.TrimSuffix(path, "/") + "/..."
}

func resolve(dir, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(dir, path)
}
