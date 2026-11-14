package importer

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/KaosSpectrum/perforce2lore/internal/lore"
)

// tagLine matches a "#tag" or "#tag value" description line.
var tagLine = regexp.MustCompile(`^\s*#([A-Za-z][\w.\-]*)(?:[ \t]+(.*?))?\s*$`)

// Message builds a revision message from a changelist description and returns its tags as metadata.
// Tags sharing a key are joined with ", " and a tag without a value is "true".
func (c *Config) Message(change int, description string) (string, []lore.Field) {
	var kept []string
	var tags []lore.Field
	index := map[string]int{}
	for _, line := range strings.Split(strings.ReplaceAll(description, "\r\n", "\n"), "\n") {
		if c.stripped(line) {
			continue
		}
		if c.DescriptionTags != TagsKeep {
			if m := tagLine.FindStringSubmatch(line); m != nil {
				if key, ok := c.tagKey(m[1]); ok {
					if key != "" {
						value := m[2]
						if value == "" {
							value = "true"
						}
						if i, ok := index[strings.ToLower(key)]; ok {
							tags[i].Value += ", " + value
						} else {
							index[strings.ToLower(key)] = len(tags)
							tags = append(tags, lore.Field{Key: key, Value: value})
						}
					}
					if c.DescriptionTags == TagsMove {
						continue
					}
				}
			}
		}
		kept = append(kept, strings.TrimRight(line, " \t"))
	}
	text := strings.TrimSpace(collapseBlankLines(kept))
	if text == "" {
		text = fmt.Sprintf("Changelist %d", change)
	}
	return text, tags
}

// tagKey returns the metadata key for a tag; an empty key drops it, and false leaves the line in the message.
func (c *Config) tagKey(tag string) (string, bool) {
	if key, ok := c.tagMap[strings.ToLower(tag)]; ok {
		return key, true
	}
	if c.OnlyMappedTags {
		return "", false
	}
	return c.TagPrefix + tag, true
}

func (c *Config) stripped(line string) bool {
	for _, re := range c.stripLines {
		if re.MatchString(line) {
			return true
		}
	}
	return false
}

// collapseBlankLines joins lines, keeping at most one blank line in a row.
func collapseBlankLines(lines []string) string {
	var b strings.Builder
	blank := false
	for _, line := range lines {
		if line == "" {
			if blank {
				continue
			}
			blank = true
		} else {
			blank = false
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}
