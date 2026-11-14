package importer

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// state is the progress saved between runs.
type state struct {
	// LastChange is the last changelist imported from a depot path.
	LastChange int `json:"lastChange,omitempty"`
	// Streams is the last changelist imported from each stream.
	Streams map[string]int `json:"streams,omitempty"`
}

// last returns the last changelist imported for a stream, or for the depot path when stream is empty.
func (s *state) last(stream string) int {
	if stream == "" {
		return s.LastChange
	}
	return s.Streams[stream]
}

// set records the last changelist imported for a stream, or for the depot path when stream is empty.
func (s *state) set(stream string, change int) {
	if stream == "" {
		s.LastChange = change
		return
	}
	if s.Streams == nil {
		s.Streams = map[string]int{}
	}
	s.Streams[stream] = change
}

// loadState reads the state file; a missing file is an empty state.
func loadState(path string) (*state, error) {
	s := &state{}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, s); err != nil {
		return nil, err
	}
	return s, nil
}

// saveState writes the state file, replacing it in one step.
func saveState(path string, s *state) error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// pruneEmptyDirs removes empty directories under root, deepest first, and returns how many it removed.
// The root and the .lore directory are kept.
func pruneEmptyDirs(root string) (int, error) {
	root = filepath.Clean(root)
	var dirs []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() || path == root {
			return nil
		}
		if strings.EqualFold(d.Name(), ".lore") && filepath.Dir(path) == root {
			return filepath.SkipDir
		}
		dirs = append(dirs, path)
		return nil
	})
	if err != nil {
		return 0, err
	}
	return removeEmpty(dirs)
}

// removeEmpty removes the directories that are empty, children before parents when listed in walk order.
func removeEmpty(dirs []string) (int, error) {
	removed := 0
	for i := len(dirs) - 1; i >= 0; i-- {
		entries, err := os.ReadDir(dirs[i])
		if err != nil || len(entries) > 0 {
			continue
		}
		if err := os.Remove(dirs[i]); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

// removeUntracked deletes files under root that are not in keep, then any directories left empty.
// keep holds local paths; on Windows they are compared ignoring case.
func removeUntracked(root string, keep []string) (int, error) {
	root = filepath.Clean(root)
	known := make(map[string]bool, len(keep))
	for _, p := range keep {
		known[pathKey(p)] = true
	}
	var dirs []string
	removed := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		if d.IsDir() {
			if strings.EqualFold(d.Name(), ".lore") && filepath.Dir(path) == root {
				return filepath.SkipDir
			}
			dirs = append(dirs, path)
			return nil
		}
		if known[pathKey(path)] {
			return nil
		}
		if err := os.Remove(path); err != nil {
			return err
		}
		removed++
		return nil
	})
	if err != nil {
		return removed, err
	}
	_, err = removeEmpty(dirs)
	return removed, err
}

func pathKey(path string) string {
	path = filepath.Clean(path)
	if runtime.GOOS == "windows" {
		return strings.ToLower(path)
	}
	return path
}
