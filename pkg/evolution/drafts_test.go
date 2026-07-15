package evolution_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/evolution"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/skills"
)

func TestDefaultDraftGenerator_PrefersLateAddedSkillAsTargetWhenNoMatches(t *testing.T) {
	generator := evolution.NewDefaultDraftGenerator(t.TempDir())

	draft, err := generator.GenerateDraft(context.Background(), evolution.LearningRecord{
		Summary:              "weather lookup",
		WinningPath:          []string{"weather"},
		LateAddedSkills:      []string{"weather"},
		FinalSnapshotTrigger: "context_retry_rebuild",
		EventCount:           4,
		SuccessRate:          1,
	}, nil)
	if err != nil {
		t.Fatalf("GenerateDraft: %v", err)
	}
	if draft.TargetSkillName != "weather" {
		t.Fatalf("TargetSkillName = %q, want weather", draft.TargetSkillName)
	}
	if !strings.Contains(draft.BodyOrPatch, "Late-added skill") {
		t.Fatalf("BodyOrPatch = %q, want late-added skill guidance", draft.BodyOrPatch)
	}
}

func TestDefaultDraftGenerator_PrefersCombinedSkillForStableMultiSkillPath(t *testing.T) {
	workspace := t.TempDir()
	generator := evolution.NewDefaultDraftGenerator(workspace)

	draft, err := generator.GenerateDraft(context.Background(), evolution.LearningRecord{
		Summary:         "调用三一定理计算100",
		WinningPath:     []string{"three-one-theorem", "four-two-theorem", "five-three-theorem"},
		LateAddedSkills: []string{"three-one-theorem", "four-two-theorem", "five-three-theorem"},
		EventCount:      3,
		SuccessRate:     1,
	}, []skills.SkillInfo{
		{
			Name:   "three-one-theorem",
			Path:   filepath.Join(workspace, "skills", "three-one-theorem", "SKILL.md"),
			Source: "workspace",
		},
		{
			Name:   "four-two-theorem",
			Path:   filepath.Join(workspace, "skills", "four-two-theorem", "SKILL.md"),
			Source: "workspace",
		},
		{
			Name:   "five-three-theorem",
			Path:   filepath.Join(workspace, "skills", "five-three-theorem", "SKILL.md"),
			Source: "workspace",
		},
	})
	if err != nil {
		t.Fatalf("GenerateDraft: %v", err)
	}
	if draft.TargetSkillName != "calculate-100-via-theorems" {
		t.Fatalf("TargetSkillName = %q, want calculate-100-via-theorems", draft.TargetSkillName)
	}
	if draft.ChangeKind != evolution.ChangeKindCreate {
		t.Fatalf("ChangeKind = %q, want create", draft.ChangeKind)
	}
	if !strings.Contains(draft.BodyOrPatch, "---\nname: calculate-100-via-theorems") {
		t.Fatalf("BodyOrPatch should contain full skill document:\n%s", draft.BodyOrPatch)
	}
}

func TestDefaultDraftGenerator_CombinedSkillIncludesEvidenceAndSourceOperations(t *testing.T) {
	workspace := t.TempDir()
	generator := evolution.NewDefaultDraftGenerator(workspace)
	sourceSkills := []struct {
		name string
		body string
	}{
		{name: "three-one-theorem", body: "Add 31 to the input value."},
		{name: "four-two-theorem", body: "Add 42 to the current value."},
		{name: "five-three-theorem", body: "Subtract 53 from the current value."},
	}

	matches := make([]skills.SkillInfo, 0, len(sourceSkills))
	for _, source := range sourceSkills {
		skillPath := filepath.Join(workspace, "skills", source.name, "SKILL.md")
		if err := os.MkdirAll(filepath.Dir(skillPath), 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		content := "---\nname: " + source.name + "\ndescription: theorem helper\n---\n# " + source.name + "\n" + source.body + "\n"
		if err := os.WriteFile(skillPath, []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		matches = append(
			matches,
			skills.SkillInfo{Name: source.name, Path: skillPath, Source: "workspace", Description: "theorem helper"},
		)
	}

	draft, err := generator.GenerateDraftWithEvidence(context.Background(), evolution.LearningRecord{
		ID:            "pattern-1",
		Summary:       "调用三一定理计算100",
		TaskRecordIDs: []string{"task-1"},
	}, matches, evolution.DraftEvidence{
		TaskRecords: []evolution.LearningRecord{
			{
				ID:             "task-1",
				Summary:        "调用三一定理计算100",
				FinalOutput:    "100 + 31 = 131; 131 + 42 = 173; 173 - 53 = 120",
				UsedSkillNames: []string{"three-one-theorem", "four-two-theorem", "five-three-theorem"},
			},
		},
	})
	if err != nil {
		t.Fatalf("GenerateDraftWithEvidence: %v", err)
	}
	for _, want := range []string{
		"calculate-100-via-theorems",
		"Add 31 to the input value",
		"Add 42 to the current value",
		"Subtract 53 from the current value",
		"100 + 31 = 131",
		"task-1",
	} {
		if !strings.Contains(draft.BodyOrPatch, want) && draft.TargetSkillName != want {
			t.Fatalf("draft missing %q:\nname=%s\n%s", want, draft.TargetSkillName, draft.BodyOrPatch)
		}
	}
}

func TestDefaultDraftGenerator_DoesNotInferNumericOnlyTargetFromSummary(t *testing.T) {
	generator := evolution.NewDefaultDraftGenerator(t.TempDir())

	draft, err := generator.GenerateDraft(context.Background(), evolution.LearningRecord{
		Summary:     "100",
		EventCount:  1,
		SuccessRate: 1,
	}, nil)
	if err != nil {
		t.Fatalf("GenerateDraft: %v", err)
	}
	if draft.TargetSkillName != "learned-100" {
		t.Fatalf("TargetSkillName = %q, want learned-100", draft.TargetSkillName)
	}
}

func TestDefaultDraftGenerator_UsesCompleteReplacementWhenExtendingExistingSkill(t *testing.T) {
	workspace := t.TempDir()
	generator := evolution.NewDefaultDraftGenerator(workspace)

	existingPath := filepath.Join(workspace, "skills", "weather", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(existingPath), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	existing := "---\nname: weather\ndescription: weather helper\n---\n# Weather\n## Start Here\nUse city names.\n"
	if err := os.WriteFile(existingPath, []byte(existing), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	draft, err := generator.GenerateDraft(context.Background(), evolution.LearningRecord{
		Summary:     "weather native-name path",
		WinningPath: []string{"weather"},
		EventCount:  4,
		SuccessRate: 1,
	}, []skills.SkillInfo{
		{Name: "weather", Path: existingPath, Source: "workspace", Description: "Weather helper"},
	})
	if err != nil {
		t.Fatalf("GenerateDraft: %v", err)
	}
	if draft.ChangeKind != evolution.ChangeKindReplace {
		t.Fatalf("ChangeKind = %q, want replace", draft.ChangeKind)
	}
	if !strings.Contains(draft.BodyOrPatch, "---\nname: weather") {
		t.Fatalf("BodyOrPatch should be a complete skill document:\n%s", draft.BodyOrPatch)
	}
	if !strings.Contains(draft.BodyOrPatch, "Use city names.") {
		t.Fatalf("BodyOrPatch lost existing guidance:\n%s", draft.BodyOrPatch)
	}
	if !strings.Contains(draft.BodyOrPatch, "## Learned Evolution") {
		t.Fatalf("BodyOrPatch = %q, want learned evolution section", draft.BodyOrPatch)
	}
	if len(draft.IntendedUseCases) != 1 || draft.IntendedUseCases[0] != "weather native-name path" {
		t.Fatalf("IntendedUseCases = %v, want [weather native-name path]", draft.IntendedUseCases)
	}
	if len(draft.PreferredEntryPath) != 1 || draft.PreferredEntryPath[0] != "weather" {
		t.Fatalf("PreferredEntryPath = %v, want [weather]", draft.PreferredEntryPath)
	}
}

func TestDefaultDraftGenerator_ReplacesPriorLearnedEvolutionSection(t *testing.T) {
	workspace := t.TempDir()
	generator := evolution.NewDefaultDraftGenerator(workspace)
	existingPath := filepath.Join(workspace, "skills", "weather", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(existingPath), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	existing := "---\nname: weather\ndescription: weather helper\n---\n# Weather\n## Safety\nNever expose credentials.\n\n## Learned Evolution\n- Summary: obsolete pattern\n"
	if err := os.WriteFile(existingPath, []byte(existing), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	draft, err := generator.GenerateDraft(context.Background(), evolution.LearningRecord{
		Summary: "prefer native-name lookup", WinningPath: []string{"weather"},
	}, []skills.SkillInfo{{Name: "weather", Path: existingPath}})
	if err != nil {
		t.Fatalf("GenerateDraft: %v", err)
	}
	if strings.Count(draft.BodyOrPatch, "## Learned Evolution") != 1 {
		t.Fatalf("replacement accumulated learned sections:\n%s", draft.BodyOrPatch)
	}
	for _, want := range []string{"Never expose credentials.", "prefer native-name lookup"} {
		if !strings.Contains(draft.BodyOrPatch, want) {
			t.Fatalf("replacement missing %q:\n%s", want, draft.BodyOrPatch)
		}
	}
}

func TestLLMDraftGenerator_BuildPromptIncludesLateAddedSkillHint(t *testing.T) {
	provider := &llmDraftTestProvider{
		defaultModel: "test-model",
		response: &providers.LLMResponse{
			Content: `{"target_skill_name":"weather","draft_type":"shortcut","change_kind":"append","human_summary":"Prefer native-name lookup first","body_or_patch":"## Start Here\nUse native-name first."}`,
		},
	}
	generator := evolution.NewLLMDraftGenerator(provider, "", &recordingDraftGenerator{})

	_, err := generator.GenerateDraft(context.Background(), evolution.LearningRecord{
		ID:                   "rule-1",
		Summary:              "weather native-name path",
		EventCount:           7,
		SuccessRate:          0.86,
		WinningPath:          []string{"geocode", "weather"},
		MatchedSkillNames:    []string{"weather"},
		LateAddedSkills:      []string{"weather"},
		FinalSnapshotTrigger: "context_retry_rebuild",
	}, []skills.SkillInfo{
		{Name: "weather", Path: "/tmp/weather/SKILL.md", Source: "workspace", Description: "Find weather details."},
	})
	if err != nil {
		t.Fatalf("GenerateDraft: %v", err)
	}

	prompt := provider.lastMessages[1].Content
	if !strings.Contains(prompt, `"late_added_skills":`) || !strings.Contains(prompt, `"weather"`) {
		t.Fatalf("prompt missing late-added skill hint:\n%s", prompt)
	}
	if !strings.Contains(prompt, `"final_snapshot_trigger": "context_retry_rebuild"`) {
		t.Fatalf("prompt missing final snapshot trigger:\n%s", prompt)
	}
	if !strings.Contains(prompt, "prefer a new combined shortcut skill") {
		t.Fatalf("prompt missing combined skill guidance:\n%s", prompt)
	}
	if !strings.Contains(prompt, `"inferred_target": "weather-native-name-path-shortcut"`) {
		t.Fatalf("prompt missing suggested target skill name:\n%s", prompt)
	}
}
