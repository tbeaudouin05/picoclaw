package evolution

// boundedLearningRecord is the persistence and cold-path trust boundary. Runtime
// transcripts and provider output can be arbitrarily large, so retain only compact
// evidence that is useful to clustering, judging, and drafting.
func boundedLearningRecord(r LearningRecord) LearningRecord {
	r.ID = summarizeText(r.ID, 160)
	r.WorkspaceID = summarizeText(r.WorkspaceID, 300)
	r.SessionKey = summarizeText(r.SessionKey, 160)
	r.TaskHash = summarizeText(r.TaskHash, 160)
	r.Summary = summarizeText(r.Summary, 600)
	r.UserGoal = summarizeText(r.UserGoal, 1200)
	r.FinalOutput = summarizeText(r.FinalOutput, 2400)
	r.TurnStatus = summarizeText(r.TurnStatus, 80)
	r.Label = summarizeText(r.Label, 160)
	r.ClusterReason = summarizeText(r.ClusterReason, 600)
	r.FinalSnapshotTrigger = summarizeText(r.FinalSnapshotTrigger, 160)
	r.Source = nil // provider/runtime maps have no stable bounded schema
	r.ToolKinds = boundedStrings(r.ToolKinds, 32, 120)
	r.InitialSkillNames = boundedStrings(r.InitialSkillNames, 32, 120)
	r.AddedSkillNames = boundedStrings(r.AddedSkillNames, 32, 120)
	r.UsedSkillNames = boundedStrings(r.UsedSkillNames, 32, 120)
	r.AllLoadedSkillNames = boundedStrings(r.AllLoadedSkillNames, 64, 120)
	r.ActiveSkillNames = boundedStrings(r.ActiveSkillNames, 32, 120)
	r.Signals = boundedStrings(r.Signals, 32, 240)
	r.SourceRecordIDs = boundedStrings(r.SourceRecordIDs, 256, 160)
	r.TaskRecordIDs = boundedStrings(r.TaskRecordIDs, 256, 160)
	r.WinningPath = boundedStrings(r.WinningPath, 32, 120)
	r.LateAddedSkills = boundedStrings(r.LateAddedSkills, 32, 120)
	r.MatchedSkillNames = boundedStrings(r.MatchedSkillNames, 32, 120)
	if len(r.ToolExecutions) > 64 {
		r.ToolExecutions = r.ToolExecutions[:64]
	}
	for i := range r.ToolExecutions {
		r.ToolExecutions[i].Name = summarizeText(r.ToolExecutions[i].Name, 120)
		r.ToolExecutions[i].ErrorSummary = summarizeText(r.ToolExecutions[i].ErrorSummary, 300)
		r.ToolExecutions[i].SkillNames = boundedStrings(r.ToolExecutions[i].SkillNames, 16, 120)
	}
	if r.AttemptTrail != nil {
		r.AttemptTrail.AttemptedSkills = boundedStrings(r.AttemptTrail.AttemptedSkills, 32, 120)
		r.AttemptTrail.FinalSuccessfulPath = boundedStrings(r.AttemptTrail.FinalSuccessfulPath, 32, 120)
		if len(r.AttemptTrail.SkillContextSnapshots) > 32 {
			r.AttemptTrail.SkillContextSnapshots = r.AttemptTrail.SkillContextSnapshots[:32]
		}
		for i := range r.AttemptTrail.SkillContextSnapshots {
			s := &r.AttemptTrail.SkillContextSnapshots[i]
			s.Trigger = summarizeText(s.Trigger, 160)
			s.SkillNames = boundedStrings(s.SkillNames, 32, 120)
		}
	}
	if r.Enrichment != nil {
		boundEnrichment(r.Enrichment)
	}
	return r
}

func boundedStrings(values []string, count, chars int) []string {
	if len(values) > count {
		values = values[:count]
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = summarizeText(value, chars); value != "" {
			out = append(out, value)
		}
	}
	return out
}

func boundEnrichment(e *TaskRecordEnrichment) {
	e.Summary = summarizeText(e.Summary, 300)
	e.TaskType = summarizeText(e.TaskType, 80)
	e.OutcomeOrBlocker = summarizeText(e.OutcomeOrBlocker, 300)
	groups := []*[]EvidenceAssessment{&e.TopFrictionsErrors, &e.ProcessImprovements, &e.ReusableKnowledge}
	for _, group := range groups {
		if len(*group) > 3 {
			*group = (*group)[:3]
		}
		for i := range *group {
			(*group)[i].Text = summarizeText((*group)[i].Text, 300)
			(*group)[i].Evidence = summarizeText((*group)[i].Evidence, 400)
		}
	}
	e.LearningValue.Text = summarizeText(e.LearningValue.Text, 300)
	e.LearningValue.Evidence = summarizeText(e.LearningValue.Evidence, 400)
}
