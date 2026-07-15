package evolution

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/sipeed/picoclaw/pkg/fileutil"
	"github.com/sipeed/picoclaw/pkg/skills"
)

type Applier struct {
	paths Paths
	now   func() time.Time
}

func NewApplier(paths Paths, now func() time.Time) *Applier {
	if now == nil {
		now = time.Now
	}
	return &Applier{
		paths: paths,
		now:   now,
	}
}

func (a *Applier) ApplyDraft(ctx context.Context, workspace string, draft SkillDraft) error {
	rollback, err := a.applyDraftWithRollback(ctx, workspace, draft)
	if err != nil {
		return err
	}
	_ = rollback
	return nil
}

func (a *Applier) applyDraftWithRollback(
	ctx context.Context,
	workspace string,
	draft SkillDraft,
) (func() error, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	if validateErr := skills.ValidateSkillName(draft.TargetSkillName); validateErr != nil {
		return nil, validateErr
	}
	skillPath := filepath.Join(workspace, "skills", draft.TargetSkillName, "SKILL.md")
	if draft.ChangeKind == ChangeKindAppend || draft.ChangeKind == ChangeKindMerge {
		if _, statErr := os.Stat(skillPath); statErr == nil {
			return nil, fmt.Errorf(
				"cannot %s existing skill %q: evolution updates require a complete replacement",
				draft.ChangeKind,
				draft.TargetSkillName,
			)
		} else if !os.IsNotExist(statErr) {
			return nil, statErr
		}
	}

	existingBody, backupPath, hadOriginal, err := a.backupCurrentSkill(workspace, draft.TargetSkillName)
	if err != nil {
		return nil, err
	}
	if hadOriginal && (draft.ChangeKind == ChangeKindAppend || draft.ChangeKind == ChangeKindMerge) {
		return nil, fmt.Errorf(
			"cannot %s existing skill %q: evolution updates require a complete replacement",
			draft.ChangeKind,
			draft.TargetSkillName,
		)
	}
	renderedBody, err := renderAppliedBody(draft, existingBody, hadOriginal)
	if err != nil {
		return nil, err
	}

	if err := validateAppliedSkillBody(
		renderedBody,
		draft.TargetSkillName,
		allowsExistingFrontmatterFields(draft.ChangeKind, hadOriginal),
	); err != nil {
		return nil, err
	}
	if hadOriginal && draft.ChangeKind == ChangeKindReplace {
		if err := validateHolisticReplacement(existingBody, renderedBody); err != nil {
			return nil, fmt.Errorf("unsafe incomplete replacement: %w", err)
		}
	}

	skillDir := filepath.Join(workspace, "skills", draft.TargetSkillName)
	if mkdirErr := os.MkdirAll(skillDir, 0o755); mkdirErr != nil {
		return nil, mkdirErr
	}

	if err := fileutil.WriteFileAtomic(skillPath, []byte(renderedBody), 0o644); err != nil {
		return nil, err
	}

	return func() error {
		return a.rollbackSkill(skillPath, backupPath, hadOriginal)
	}, nil
}

func validateHolisticReplacement(existing, candidate string) error {
	existingFM, existingMD := splitSkillFrontmatter(existing)
	candidateFM, candidateMD := splitSkillFrontmatter(candidate)
	existingFields, err := parseSkillFrontmatterFields(existingFM, true)
	if err != nil {
		return fmt.Errorf("existing frontmatter: %w", err)
	}
	candidateFields, err := parseSkillFrontmatterFields(candidateFM, true)
	if err != nil {
		return fmt.Errorf("candidate frontmatter: %w", err)
	}
	for key := range existingFields {
		if _, ok := candidateFields[key]; !ok {
			return fmt.Errorf("required frontmatter field %q was removed", key)
		}
	}
	visibleExisting := visibleOperationalMarkdown(existingMD)
	visibleCandidate := visibleOperationalMarkdown(candidateMD)
	existingH1 := firstMarkdownHeading(visibleExisting, "# ")
	candidateH1 := firstMarkdownHeading(visibleCandidate, "# ")
	if existingH1 != "" && !strings.EqualFold(existingH1, candidateH1) {
		return fmt.Errorf("root heading %q was not preserved", existingH1)
	}
	oldLen, newLen := len(strings.TrimSpace(existingMD)), len(strings.TrimSpace(candidateMD))
	if oldLen >= 300 && newLen < oldLen*60/100 {
		return fmt.Errorf("candidate is an obvious minimal replacement (%d bytes versus %d)", newLen, oldLen)
	}
	for _, heading := range substantialSectionHeadings(visibleExisting) {
		if !hasMarkdownHeading(visibleCandidate, heading) {
			return fmt.Errorf("substantial section %q was removed", heading)
		}
	}
	candidateNorm := normalizeConstraintText(visibleCandidate)
	for _, constraint := range safetyConstraintLines(visibleExisting) {
		if !strings.Contains(candidateNorm, normalizeConstraintText(constraint)) {
			return fmt.Errorf("safety constraint was removed: %q", trimAtReadableBoundary(strings.TrimSpace(constraint), 120))
		}
	}
	return nil
}

// visibleOperationalMarkdown removes content that is not rendered as operative
// prose. Safety requirements preserved only in comments or examples do not
// protect users of the skill and therefore cannot satisfy replacement checks.
func visibleOperationalMarkdown(md string) string {
	lines := strings.Split(md, "\n")
	visible := make([]string, 0, len(lines))
	inFence := false
	fenceChar := byte(0)
	fenceLen := 0
	inComment := false
	for _, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")
		if !inComment {
			if char, count := markdownFence(trimmed); count >= 3 {
				if !inFence {
					inFence, fenceChar, fenceLen = true, char, count
					continue
				}
				if char == fenceChar && count >= fenceLen {
					inFence = false
				}
				continue
			}
		}
		if inFence {
			continue
		}
		var out strings.Builder
		for len(line) > 0 {
			if inComment {
				end := strings.Index(line, "-->")
				if end < 0 {
					line = ""
					break
				}
				line, inComment = line[end+3:], false
				continue
			}
			start := strings.Index(line, "<!--")
			if start < 0 {
				out.WriteString(line)
				break
			}
			out.WriteString(line[:start])
			line, inComment = line[start+4:], true
		}
		visible = append(visible, out.String())
	}
	return strings.Join(visible, "\n")
}

func markdownFence(line string) (byte, int) {
	if line == "" || (line[0] != '`' && line[0] != '~') {
		return 0, 0
	}
	char := line[0]
	count := 0
	for count < len(line) && line[count] == char {
		count++
	}
	return char, count
}

func firstMarkdownHeading(md, prefix string) string {
	for _, line := range strings.Split(md, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, prefix) && !strings.HasPrefix(line, prefix+"#") {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	return ""
}
func hasMarkdownHeading(md, heading string) bool {
	for _, line := range strings.Split(md, "\n") {
		if strings.EqualFold(strings.TrimSpace(strings.TrimLeft(line, "#")), heading) && strings.HasPrefix(strings.TrimSpace(line), "##") {
			return true
		}
	}
	return false
}
func substantialSectionHeadings(md string) []string {
	lines := strings.Split(md, "\n")
	out := []string{}
	heading := ""
	size := 0
	flush := func() {
		if heading != "" && size >= 160 {
			out = append(out, heading)
		}
	}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## ") {
			flush()
			heading = strings.TrimSpace(strings.TrimPrefix(trimmed, "## "))
			size = 0
		} else if heading != "" {
			size += len(trimmed)
		}
	}
	flush()
	return out
}
func safetyConstraintLines(md string) []string {
	keywords := []string{"must ", "must not", "never ", "do not", "don't ", "only ", "avoid ", "required", "ensure ", "warning", "caution", "safety", "permission", "secret", "credential"}
	out := []string{}
	for _, line := range strings.Split(md, "\n") {
		plain := strings.ToLower(strings.TrimSpace(strings.TrimLeft(line, "-*0123456789. ")))
		if len(plain) < 12 {
			continue
		}
		for _, word := range keywords {
			if strings.Contains(plain, word) {
				out = append(out, plain)
				break
			}
		}
	}
	return out
}
func normalizeConstraintText(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

func (a *Applier) backupCurrentSkill(
	workspace, skillName string,
) (currentBody, backupPath string, hadOriginal bool, err error) {
	if validateErr := skills.ValidateSkillName(skillName); validateErr != nil {
		return "", "", false, validateErr
	}

	skillPath := filepath.Join(workspace, "skills", skillName, "SKILL.md")
	data, err := os.ReadFile(skillPath)
	if os.IsNotExist(err) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}

	backupDir := filepath.Join(
		a.paths.BackupsDir,
		workspaceScopeDir(workspace),
		skillName,
		a.now().Format("20060102-150405.000000000"),
	)
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return "", "", false, err
	}

	backupPath = filepath.Join(backupDir, "SKILL.md")
	if err := fileutil.WriteFileAtomic(backupPath, data, 0o644); err != nil {
		return "", "", false, err
	}
	return string(data), backupPath, true, nil
}

func (a *Applier) rollbackSkill(skillPath, backupPath string, hadOriginal bool) error {
	if hadOriginal {
		data, err := os.ReadFile(backupPath)
		if err != nil {
			return err
		}
		return fileutil.WriteFileAtomic(skillPath, data, 0o644)
	}
	if err := os.Remove(skillPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	skillDir := filepath.Dir(skillPath)
	if err := os.Remove(skillDir); err != nil && !os.IsNotExist(err) && !isDirNotEmptyError(err) {
		return err
	}
	return nil
}

func isDirNotEmptyError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "directory not empty")
}

func validateAppliedSkillBody(body, targetSkillName string, allowExtraFrontmatterFields bool) error {
	body = strings.TrimSpace(body)
	if !strings.HasPrefix(body, "---\n") {
		return fmt.Errorf("skill frontmatter is required")
	}
	if !strings.Contains(body, "\n# ") {
		return fmt.Errorf("skill heading is required")
	}
	frontmatter, _ := splitSkillFrontmatter(body)
	fields, err := parseSkillFrontmatterFields(frontmatter, allowExtraFrontmatterFields)
	if err != nil {
		return err
	}
	name := strings.TrimSpace(fields["name"])
	if name == "" {
		return fmt.Errorf("skill frontmatter name is required")
	}
	if name != targetSkillName {
		return fmt.Errorf("skill frontmatter name %q does not match target skill %q", name, targetSkillName)
	}
	if strings.TrimSpace(fields["description"]) == "" {
		return fmt.Errorf("skill frontmatter description is required")
	}
	return nil
}

func allowsExistingFrontmatterFields(kind ChangeKind, hadOriginal bool) bool {
	return hadOriginal && kind == ChangeKindReplace
}

func renderAppliedBody(draft SkillDraft, existingBody string, hadOriginal bool) (string, error) {
	switch draft.ChangeKind {
	case ChangeKindCreate:
		if hadOriginal {
			return "", fmt.Errorf("cannot create skill %q: skill already exists", draft.TargetSkillName)
		}
		return renderDeployableSkillBody(draft.BodyOrPatch), nil
	case ChangeKindReplace:
		if !hadOriginal {
			return "", fmt.Errorf("cannot replace skill %q: skill does not exist", draft.TargetSkillName)
		}
		return renderDeployableSkillBody(draft.BodyOrPatch), nil
	case ChangeKindAppend:
		patch, err := renderDeployablePatchBody(draft.BodyOrPatch, draft.TargetSkillName)
		if err != nil {
			return "", err
		}
		if !hadOriginal || strings.TrimSpace(existingBody) == "" {
			return renderDeployableSkillBody(draft.BodyOrPatch), nil
		}
		return strings.TrimRight(existingBody, "\n") + "\n\n" + strings.TrimLeft(patch, "\n"), nil
	case ChangeKindMerge:
		patch, err := renderDeployablePatchBody(draft.BodyOrPatch, draft.TargetSkillName)
		if err != nil {
			return "", err
		}
		if !hadOriginal || strings.TrimSpace(existingBody) == "" {
			return renderDeployableSkillBody(draft.BodyOrPatch), nil
		}
		mergedSection := strings.Join([]string{
			"",
			"## Merged Knowledge",
			strings.TrimSpace(patch),
			"",
		}, "\n")
		return strings.TrimRight(existingBody, "\n") + mergedSection, nil
	default:
		return "", fmt.Errorf("unsupported change_kind %q", draft.ChangeKind)
	}
}

func renderDeployablePatchBody(body, targetSkillName string) (string, error) {
	body = renderDeployableSkillBody(body)
	frontmatter, markdownBody := splitSkillFrontmatter(body)
	if frontmatter == "" {
		markdownBody = body
	} else {
		fields, err := parseSkillFrontmatterFields(frontmatter, true)
		if err != nil {
			return "", err
		}
		if name := strings.TrimSpace(fields["name"]); name != "" && name != targetSkillName {
			return "", fmt.Errorf(
				"skill patch frontmatter name %q does not match target skill %q",
				name,
				targetSkillName,
			)
		}
	}
	return strings.TrimSpace(stripLeadingH1(markdownBody)), nil
}

func splitSkillFrontmatter(body string) (frontmatter, markdownBody string) {
	normalized := strings.ReplaceAll(strings.TrimSpace(body), "\r\n", "\n")
	lines := strings.Split(normalized, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return "", body
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return "", body
	}
	return strings.Join(lines[1:end], "\n"), strings.TrimLeft(strings.Join(lines[end+1:], "\n"), "\n")
}

func parseSkillFrontmatterFields(frontmatter string, allowExtraFields bool) (map[string]string, error) {
	var raw map[string]any
	if err := yaml.Unmarshal([]byte(frontmatter), &raw); err != nil {
		return nil, fmt.Errorf("invalid skill frontmatter: %w", err)
	}
	for key := range raw {
		if key != "name" && key != "description" {
			if allowExtraFields {
				continue
			}
			return nil, fmt.Errorf("unsupported skill frontmatter field %q", key)
		}
	}

	var typed struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}
	if err := yaml.Unmarshal([]byte(frontmatter), &typed); err != nil {
		return nil, fmt.Errorf("invalid skill frontmatter: %w", err)
	}
	return map[string]string{
		"name":        typed.Name,
		"description": typed.Description,
	}, nil
}

func stripLeadingH1(body string) string {
	lines := strings.Split(strings.TrimLeft(body, "\n"), "\n")
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	if len(lines) > 0 && strings.HasPrefix(strings.TrimSpace(lines[0]), "# ") {
		lines = lines[1:]
	}
	return strings.Join(lines, "\n")
}

func errorsJoin(errs ...error) error {
	var first error
	for _, err := range errs {
		if err == nil {
			continue
		}
		if first == nil {
			first = err
			continue
		}
		first = fmt.Errorf("%w; %v", first, err)
	}
	return first
}
