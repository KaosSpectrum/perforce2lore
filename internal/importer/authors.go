package importer

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/KaosSpectrum/perforce2lore/internal/p4"
)

// MissingAuthorError stops the import until the authors file credits the changelist.
type MissingAuthorError struct {
	Change int
	User   string
}

func (e *MissingAuthorError) Error() string {
	return fmt.Sprintf("changelist %d: Perforce user %q has no Lore user in the authors file", e.Change, e.User)
}

// Authors maps lower-case names to Lore users.
type Authors map[string]string

// LoadAuthors reads the authors file; a missing file is an empty map and blank values are unmapped.
func LoadAuthors(path string) (Authors, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Authors{}, nil
	}
	if err != nil {
		return nil, err
	}
	raw := map[string]string{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	authors := make(Authors, len(raw))
	for k, v := range raw {
		if v = strings.TrimSpace(v); v != "" {
			authors[strings.ToLower(strings.TrimSpace(k))] = v
		}
	}
	return authors, nil
}

func (a Authors) lookup(key string) (string, bool) {
	if key == "" {
		return "", false
	}
	id, ok := a[strings.ToLower(key)]
	return id, ok
}

// ChangeKey is the authors file entry for one changelist.
func ChangeKey(n int) string { return fmt.Sprintf("@%d", n) }

// Credit is the Lore user a changelist is credited to and the entry that matched.
type Credit struct {
	User   string
	Source string
}

// ResolveAuthor finds the Lore user for a changelist, trying in order: the changelist entry,
// the Perforce user, the user's email and full name, and the default author.
func (c *Config) ResolveAuthor(ch p4.Change, authors Authors, users map[string]p4.User) (Credit, error) {
	keys := []string{ChangeKey(ch.Number), ch.User}
	if u, ok := users[strings.ToLower(ch.User)]; ok {
		keys = append(keys, u.Email, u.FullName)
	}
	for _, key := range keys {
		if id, ok := authors.lookup(key); ok {
			return Credit{id, key}, nil
		}
	}
	if c.DefaultAuthor != "" {
		return Credit{c.DefaultAuthor, "defaultAuthor"}, nil
	}
	return Credit{}, &MissingAuthorError{Change: ch.Number, User: ch.User}
}
