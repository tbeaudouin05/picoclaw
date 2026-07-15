package evolution

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sipeed/picoclaw/pkg/skills"
)

const (
	maxMatchedSkillExcerptCount = 5
	// Existing-skill refinement needs the complete document, not a leading excerpt.
	// Keep a prompt budget; documents beyond it are explicitly marked incomplete so
	// the evolution agent can decline an unsafe update.
	maxMatchedSkillExcerptChars = 24000
	maxComponentGuidanceChars   = 520
)

type matchedSkillExcerpt struct {
	Name        string
	Description string
	Body        string
	Complete    bool
}

func loadMatchedSkillExcerpts(matches []skills.SkillInfo) []matchedSkillExcerpt {
	excerpts := make([]matchedSkillExcerpt, 0, minInt(len(matches), maxMatchedSkillExcerptCount))
	for _, match := range matches {
		if len(excerpts) >= maxMatchedSkillExcerptCount {
			break
		}
		body, complete := readCompleteSkillDocument(match.Path)
		if body == "" && !complete {
			if _, err := os.Stat(strings.TrimSpace(match.Path)); err != nil {
				continue
			}
		}
		if body == "" && complete {
			continue
		}
		excerpts = append(excerpts, matchedSkillExcerpt{
			Name:        strings.TrimSpace(match.Name),
			Description: strings.TrimSpace(match.Description),
			Body:        body,
			Complete:    complete,
		})
	}
	return excerpts
}

func readCompleteSkillDocument(path string) (string, bool) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	body := strings.TrimSpace(string(data))
	if body == "" {
		return "", false
	}
	if len(body) <= maxMatchedSkillExcerptChars {
		return body, true
	}
	return "", false
}

func targetedSkillEvidence(rule LearningRecord, matches []skills.SkillInfo, workspace string) []matchedSkillExcerpt {
	byName := make(map[string]skills.SkillInfo)
	for _, match := range matches {
		byName[strings.TrimSpace(match.Name)] = match
	}
	names := append([]string{inferTargetSkillName(rule, matches)}, rule.MatchedSkillNames...)
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" || workspace == "" {
			continue
		}
		if _, ok := byName[name]; !ok {
			path := filepath.Join(workspace, "skills", name, "SKILL.md")
			if _, err := os.Stat(path); err == nil {
				byName[name] = skills.SkillInfo{Name: name, Path: path, Source: "workspace"}
			}
		}
	}
	ordered := make([]skills.SkillInfo, 0, len(byName))
	seen := make(map[string]bool)
	for _, name := range names {
		if info, ok := byName[strings.TrimSpace(name)]; ok && !seen[info.Name] {
			ordered = append(ordered, info)
			seen[info.Name] = true
		}
	}
	for _, match := range matches {
		if !seen[match.Name] {
			ordered = append(ordered, match)
			seen[match.Name] = true
		}
	}
	excerpts := loadMatchedSkillExcerpts(ordered)
	remaining := maxMatchedSkillExcerptChars
	for i := range excerpts {
		if !excerpts[i].Complete {
			continue
		}
		if len(excerpts[i].Body) > remaining {
			excerpts[i].Body = ""
			excerpts[i].Complete = false
			continue
		}
		remaining -= len(excerpts[i].Body)
	}
	return excerpts
}

func synthesizedComponentBreakdown(matches []skills.SkillInfo) string {
	excerpts := loadMatchedSkillExcerpts(matches)
	if len(excerpts) == 0 {
		return "- No component skill content was available when this shortcut was generated."
	}

	lines := make([]string, 0, len(excerpts))
	for _, excerpt := range excerpts {
		guidance := conciseComponentGuidance(excerpt)
		if guidance == "" {
			continue
		}
		lines = append(lines, fmt.Sprintf("- `%s`: %s", excerpt.Name, guidance))
	}
	if len(lines) == 0 {
		return "- Component skill content was available, but no concise guidance could be extracted."
	}
	return strings.Join(lines, "\n")
}

func conciseComponentGuidance(excerpt matchedSkillExcerpt) string {
	description := strings.TrimSpace(excerpt.Description)
	body := trimComponentGuidance(stripSkillFrontmatter(excerpt.Body))
	switch {
	case description != "" && body != "":
		return trimComponentGuidance(description + " " + body)
	case description != "":
		return trimComponentGuidance(description)
	default:
		return body
	}
}

func trimComponentGuidance(content string) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return ""
	}
	content = strings.NewReplacer(
		"#### ", "",
		"### ", "",
		"## ", "",
		"# ", "",
		"**", "",
	).Replace(content)
	content = strings.TrimSpace(content)
	return trimAtReadableBoundary(content, maxComponentGuidanceChars)
}
