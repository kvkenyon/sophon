// Package naming builds human-readable presentation names for tasks.
package naming

import (
	"regexp"
	"strings"
	"unicode"
)

const (
	maxTitleSlugLength = 40
	shortIDLength      = 8
)

var nonAlphanumeric = regexp.MustCompile(`[^a-z0-9]+`)

// TaskName returns a git-ref-safe, human-readable task name. It is only a
// presentation value: callers must retain taskID for all durable identity.
func TaskName(title, taskID string) string {
	slug := titleSlug(title)
	shortID := shortID(taskID)
	return slug + "-" + shortID
}

func titleSlug(title string) string {
	title = strings.Map(func(r rune) rune {
		if r <= unicode.MaxASCII {
			return unicode.ToLower(r)
		}
		return ' '
	}, title)
	slug := strings.Trim(nonAlphanumeric.ReplaceAllString(title, "-"), "-")
	if len(slug) > maxTitleSlugLength {
		slug = strings.TrimRight(slug[:maxTitleSlugLength], "-")
		if cut := strings.LastIndex(slug, "-"); cut > 0 {
			slug = slug[:cut]
		}
	}
	if slug == "" {
		return "task"
	}
	return slug
}

func shortID(taskID string) string {
	value := strings.ToLower(strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, taskID))
	if len(value) > shortIDLength {
		value = value[:shortIDLength]
	}
	if value == "" {
		return "unknown"
	}
	return value
}
