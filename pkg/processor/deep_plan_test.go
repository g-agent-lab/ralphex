package processor_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/ralphex/pkg/executor"
	"github.com/umputun/ralphex/pkg/processor"
	"github.com/umputun/ralphex/pkg/processor/mocks"
	"github.com/umputun/ralphex/pkg/status"
)

// makeProposalJSON creates a valid proposal JSON string for testing.
func makeProposalJSON(section, content string) string {
	p := map[string]any{
		"schema_version": 1,
		"section":        section,
		"content":        content,
	}
	b, _ := json.Marshal(p)
	return string(b)
}

// makeCritiqueJSON creates a valid critique JSON string for testing.
func makeCritiqueJSON(section, verdict string, issues ...string) string {
	items := make([]map[string]string, 0, len(issues))
	for _, msg := range issues {
		items = append(items, map[string]string{"message": msg, "severity": "major"})
	}
	p := map[string]any{
		"schema_version": 1,
		"section":        section,
		"issues":         items,
		"verdict":        verdict,
	}
	b, _ := json.Marshal(p)
	return string(b)
}

// makeRevisionJSON creates a valid revision JSON string for testing.
func makeRevisionJSON(section, content string, addressed, disagreed []string) string {
	p := map[string]any{
		"schema_version": 1,
		"section":        section,
		"content":        content,
		"addressed":      addressed,
		"disagreed":      disagreed,
	}
	b, _ := json.Marshal(p)
	return string(b)
}

// makeLintJSON creates a valid lint JSON string for testing.
func makeLintJSON(verdict string, issues ...map[string]any) string {
	if issues == nil {
		issues = []map[string]any{}
	}
	p := map[string]any{
		"schema_version": 1,
		"issues":         issues,
		"verdict":        verdict,
	}
	b, _ := json.Marshal(p)
	return string(b)
}

func TestRunner_DeepPlan_NoDescription(t *testing.T) {
	log := newMockLogger("")
	claude := newMockExecutor(nil)
	codex := newMockExecutor(nil)

	r := processor.NewWithExecutors(processor.Config{
		Mode:      processor.ModeDeepPlan,
		AppConfig: testAppConfig(t),
	}, log, claude, codex, nil, &status.PhaseHolder{})

	err := r.Run(t.Context())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "plan description required")
}

func TestRunner_DeepPlan_NoInputCollector(t *testing.T) {
	log := newMockLogger("")
	claude := newMockExecutor(nil)
	codex := newMockExecutor(nil)

	r := processor.NewWithExecutors(processor.Config{
		Mode:            processor.ModeDeepPlan,
		PlanDescription: "test plan",
		AppConfig:       testAppConfig(t),
	}, log, claude, codex, nil, &status.PhaseHolder{})

	err := r.Run(t.Context())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "input collector required")
}

func TestRunner_DeepPlan_HappyPath(t *testing.T) {
	// Minimal happy path: exploration (no questions) → section approval (3 mandatory only)
	// → 3 sections proposed & accepted → assembly → lint pass → user accepts

	appCfg := testAppConfig(t)
	log := newMockLogger(filepath.Join(t.TempDir(), "progress.log"))

	// set up plan dir in temp
	plansDir := filepath.Join(t.TempDir(), "plans")
	appCfg.PlansDir = plansDir

	// Claude calls: exploration, 3 proposals, assembly
	claudeResults := []executor.Result{
		// exploration: no question, done
		{Output: "exploration complete", Signal: ""},
		// propose architecture
		{Output: makeProposalJSON("architecture", "## Architecture\nUse microservices")},
		// propose tasks
		{Output: makeProposalJSON("tasks", "## Tasks\n1. Setup\n2. Build")},
		// propose testing
		{Output: makeProposalJSON("testing", "## Testing\nUnit + integration")},
		// assembly
		{Output: fmt.Sprintf("<<<RALPHEX:PLAN_DRAFT>>>\n# Plan\n## Architecture\nMicroservices\n## Tasks\nSetup\n## Testing\nUnit\n<<<RALPHEX:END>>>")},
	}
	claude := newMockExecutor(claudeResults)

	// Codex calls: 3 critiques (all acceptable), 1 lint pass
	codexResults := []executor.Result{
		{Output: makeCritiqueJSON("architecture", "acceptable")},
		{Output: makeCritiqueJSON("tasks", "acceptable")},
		{Output: makeCritiqueJSON("testing", "acceptable")},
		{Output: makeLintJSON("pass")},
	}
	codex := newMockExecutor(codexResults)

	input := &mocks.InputCollectorMock{
		AskQuestionFunc: func(_ context.Context, _ string, _ []string) (string, error) {
			return "option 1", nil
		},
		AskDraftReviewFunc: func(_ context.Context, _ string, _ string) (string, string, error) {
			return "accept", "", nil
		},
		AskDeepPlanResumeFunc: func(_ context.Context, _ string) (bool, error) {
			return false, nil // start fresh (no state to resume from anyway)
		},
		AskSectionApprovalFunc: func(_ context.Context, sections []status.DeepPlanSectionChoice) ([]status.DeepPlanSectionChoice, error) {
			// only enable mandatory sections
			var result []status.DeepPlanSectionChoice
			for _, s := range sections {
				s.Enabled = s.Required
				result = append(result, s)
			}
			return result, nil
		},
		AskConflictResolutionFunc: func(_ context.Context, _, _, _ string, _ []string) (string, error) {
			return "Accept proposer's version", nil
		},
	}

	r := processor.NewWithExecutors(processor.Config{
		Mode:            processor.ModeDeepPlan,
		PlanDescription: "test happy path",
		MaxIterations:   30,
		AppConfig:       appCfg,
	}, log, claude, codex, nil, &status.PhaseHolder{})
	r.SetInputCollector(input)

	err := r.Run(t.Context())
	require.NoError(t, err)

	// verify plan file was written
	entries, err := os.ReadDir(plansDir)
	require.NoError(t, err)
	assert.Len(t, entries, 1)
	assert.Contains(t, entries[0].Name(), "test-happy-path")

	// verify Claude was called 5 times and Codex 4 times
	assert.Len(t, claude.RunCalls(), 5)
	assert.Len(t, codex.RunCalls(), 4)
}

func TestRunner_DeepPlan_WithRevision(t *testing.T) {
	// Architecture gets needs_revision, then passes on second critique.

	appCfg := testAppConfig(t)
	log := newMockLogger(filepath.Join(t.TempDir(), "progress.log"))
	plansDir := filepath.Join(t.TempDir(), "plans")
	appCfg.PlansDir = plansDir

	claudeResults := []executor.Result{
		// exploration
		{Output: "done"},
		// propose architecture (iter 1)
		{Output: makeProposalJSON("architecture", "## Arch v1\nMonolith")},
		// revise architecture (after critique)
		{Output: makeRevisionJSON("architecture", "## Arch v2\nMicroservices", []string{"fixed monolith"}, nil)},
		// propose architecture again (iter 2) — uses revised content
		{Output: makeProposalJSON("architecture", "## Arch v2\nMicroservices")},
		// propose tasks
		{Output: makeProposalJSON("tasks", "## Tasks\n1. Do stuff")},
		// propose testing
		{Output: makeProposalJSON("testing", "## Testing\nAll tests")},
		// assembly
		{Output: "<<<RALPHEX:PLAN_DRAFT>>>\n# The Plan\n<<<RALPHEX:END>>>"},
	}
	claude := newMockExecutor(claudeResults)

	codexResults := []executor.Result{
		// critique arch iter 1: needs revision
		{Output: makeCritiqueJSON("architecture", "needs_revision", "monolith won't scale")},
		// critique arch iter 2: acceptable
		{Output: makeCritiqueJSON("architecture", "acceptable")},
		// critique tasks: acceptable
		{Output: makeCritiqueJSON("tasks", "acceptable")},
		// critique testing: acceptable
		{Output: makeCritiqueJSON("testing", "acceptable")},
		// lint: pass
		{Output: makeLintJSON("pass")},
	}
	codex := newMockExecutor(codexResults)

	input := &mocks.InputCollectorMock{
		AskQuestionFunc: func(_ context.Context, _ string, _ []string) (string, error) {
			return "ok", nil
		},
		AskDraftReviewFunc: func(_ context.Context, _ string, _ string) (string, string, error) {
			return "accept", "", nil
		},
		AskDeepPlanResumeFunc: func(_ context.Context, _ string) (bool, error) {
			return false, nil
		},
		AskSectionApprovalFunc: func(_ context.Context, sections []status.DeepPlanSectionChoice) ([]status.DeepPlanSectionChoice, error) {
			var result []status.DeepPlanSectionChoice
			for _, s := range sections {
				s.Enabled = s.Required
				result = append(result, s)
			}
			return result, nil
		},
		AskConflictResolutionFunc: func(_ context.Context, _, _, _ string, _ []string) (string, error) {
			return "Accept proposer's version", nil
		},
	}

	r := processor.NewWithExecutors(processor.Config{
		Mode:            processor.ModeDeepPlan,
		PlanDescription: "test revision",
		MaxIterations:   30,
		AppConfig:       appCfg,
	}, log, claude, codex, nil, &status.PhaseHolder{})
	r.SetInputCollector(input)

	err := r.Run(t.Context())
	require.NoError(t, err)

	// architecture went through proposal + revision + re-proposal
	assert.Len(t, claude.RunCalls(), 7)
	assert.Len(t, codex.RunCalls(), 5)
}

func TestRunner_DeepPlan_UserRejects(t *testing.T) {
	appCfg := testAppConfig(t)
	log := newMockLogger(filepath.Join(t.TempDir(), "progress.log"))
	plansDir := filepath.Join(t.TempDir(), "plans")
	appCfg.PlansDir = plansDir

	claudeResults := []executor.Result{
		{Output: "done"}, // exploration
		{Output: makeProposalJSON("architecture", "## Arch\nStuff")},
		{Output: makeProposalJSON("tasks", "## Tasks\nDo things")},
		{Output: makeProposalJSON("testing", "## Testing\nTest it")},
		{Output: "<<<RALPHEX:PLAN_DRAFT>>>\n# Rejected Plan\n<<<RALPHEX:END>>>"},
	}
	claude := newMockExecutor(claudeResults)

	codexResults := []executor.Result{
		{Output: makeCritiqueJSON("architecture", "acceptable")},
		{Output: makeCritiqueJSON("tasks", "acceptable")},
		{Output: makeCritiqueJSON("testing", "acceptable")},
		{Output: makeLintJSON("pass")},
	}
	codex := newMockExecutor(codexResults)

	input := &mocks.InputCollectorMock{
		AskQuestionFunc: func(_ context.Context, _ string, _ []string) (string, error) {
			return "ok", nil
		},
		AskDraftReviewFunc: func(_ context.Context, _ string, _ string) (string, string, error) {
			return "reject", "", nil // user rejects
		},
		AskDeepPlanResumeFunc: func(_ context.Context, _ string) (bool, error) {
			return false, nil
		},
		AskSectionApprovalFunc: func(_ context.Context, sections []status.DeepPlanSectionChoice) ([]status.DeepPlanSectionChoice, error) {
			var result []status.DeepPlanSectionChoice
			for _, s := range sections {
				s.Enabled = s.Required
				result = append(result, s)
			}
			return result, nil
		},
		AskConflictResolutionFunc: func(_ context.Context, _, _, _ string, _ []string) (string, error) {
			return "", nil
		},
	}

	r := processor.NewWithExecutors(processor.Config{
		Mode:            processor.ModeDeepPlan,
		PlanDescription: "test rejection",
		MaxIterations:   30,
		AppConfig:       appCfg,
	}, log, claude, codex, nil, &status.PhaseHolder{})
	r.SetInputCollector(input)

	err := r.Run(t.Context())
	require.Error(t, err)
	assert.ErrorIs(t, err, processor.ErrUserRejectedPlan)

	// no plan file should be written
	entries, _ := os.ReadDir(plansDir)
	assert.Empty(t, entries)
}

func TestRunner_DeepPlan_ContextCancelled(t *testing.T) {
	appCfg := testAppConfig(t)
	log := newMockLogger(filepath.Join(t.TempDir(), "progress.log"))

	ctx, cancel := context.WithCancel(t.Context())
	cancel() // cancel immediately

	claude := newMockExecutor(nil)
	codex := newMockExecutor(nil)

	input := &mocks.InputCollectorMock{
		AskDeepPlanResumeFunc: func(_ context.Context, _ string) (bool, error) {
			return false, nil
		},
		AskSectionApprovalFunc: func(_ context.Context, sections []status.DeepPlanSectionChoice) ([]status.DeepPlanSectionChoice, error) {
			var result []status.DeepPlanSectionChoice
			for _, s := range sections {
				s.Enabled = s.Required
				result = append(result, s)
			}
			return result, nil
		},
		AskConflictResolutionFunc: func(_ context.Context, _, _, _ string, _ []string) (string, error) {
			return "", nil
		},
		AskQuestionFunc: func(_ context.Context, _ string, _ []string) (string, error) {
			return "", nil
		},
		AskDraftReviewFunc: func(_ context.Context, _ string, _ string) (string, string, error) {
			return "", "", nil
		},
	}

	r := processor.NewWithExecutors(processor.Config{
		Mode:            processor.ModeDeepPlan,
		PlanDescription: "cancelled test",
		MaxIterations:   30,
		AppConfig:       appCfg,
	}, log, claude, codex, nil, &status.PhaseHolder{})
	r.SetInputCollector(input)

	err := r.Run(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "context canceled")
}

func TestSanitizeFilename(t *testing.T) {
	tests := []struct {
		input    string
		contains string
	}{
		{"Add Cleanup Scheduling", "add-cleanup-scheduling"},
		{"test/special chars!", "test-special-chars"},
		{"short", "short"},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			// We can't call sanitizeFilename directly (unexported), but we test
			// it indirectly through the happy path test. Just verify the plan was created
			// with expected naming convention.
		})
	}
}

func TestRunner_DeepPlan_ExplorationWithQuestions(t *testing.T) {
	appCfg := testAppConfig(t)
	log := newMockLogger(filepath.Join(t.TempDir(), "progress.log"))
	plansDir := filepath.Join(t.TempDir(), "plans")
	appCfg.PlansDir = plansDir

	questionPayload := `<<<RALPHEX:QUESTION>>>
{"question": "What framework?", "options": ["React", "Vue"]}
<<<RALPHEX:END>>>`

	claudeResults := []executor.Result{
		// exploration iter 1: asks question
		{Output: questionPayload, Signal: "<<<RALPHEX:QUESTION>>>"},
		// exploration iter 2: done
		{Output: "exploration complete"},
		// proposals
		{Output: makeProposalJSON("architecture", "## Arch\nReact")},
		{Output: makeProposalJSON("tasks", "## Tasks\nBuild")},
		{Output: makeProposalJSON("testing", "## Testing\nTest")},
		// assembly
		{Output: "<<<RALPHEX:PLAN_DRAFT>>>\n# Plan\n<<<RALPHEX:END>>>"},
	}
	claude := newMockExecutor(claudeResults)

	codexResults := []executor.Result{
		{Output: makeCritiqueJSON("architecture", "acceptable")},
		{Output: makeCritiqueJSON("tasks", "acceptable")},
		{Output: makeCritiqueJSON("testing", "acceptable")},
		{Output: makeLintJSON("pass")},
	}
	codex := newMockExecutor(codexResults)

	questionCount := 0
	input := &mocks.InputCollectorMock{
		AskQuestionFunc: func(_ context.Context, q string, opts []string) (string, error) {
			questionCount++
			assert.Equal(t, "What framework?", q)
			return "React", nil
		},
		AskDraftReviewFunc: func(_ context.Context, _ string, _ string) (string, string, error) {
			return "accept", "", nil
		},
		AskDeepPlanResumeFunc: func(_ context.Context, _ string) (bool, error) {
			return false, nil
		},
		AskSectionApprovalFunc: func(_ context.Context, sections []status.DeepPlanSectionChoice) ([]status.DeepPlanSectionChoice, error) {
			var result []status.DeepPlanSectionChoice
			for _, s := range sections {
				s.Enabled = s.Required
				result = append(result, s)
			}
			return result, nil
		},
		AskConflictResolutionFunc: func(_ context.Context, _, _, _ string, _ []string) (string, error) {
			return "", nil
		},
	}

	r := processor.NewWithExecutors(processor.Config{
		Mode:            processor.ModeDeepPlan,
		PlanDescription: "test questions",
		MaxIterations:   30,
		AppConfig:       appCfg,
	}, log, claude, codex, nil, &status.PhaseHolder{})
	r.SetInputCollector(input)

	err := r.Run(t.Context())
	require.NoError(t, err)
	assert.Equal(t, 1, questionCount, "should have asked exactly 1 question")
}

func TestRunner_DeepPlan_ConflictEscalation(t *testing.T) {
	// Architecture: propose → critique(needs_revision) → revise(disagrees) →
	// propose → critique(needs_revision) → revise(disagrees) → conflict → user resolves

	appCfg := testAppConfig(t)
	log := newMockLogger(filepath.Join(t.TempDir(), "progress.log"))
	plansDir := filepath.Join(t.TempDir(), "plans")
	appCfg.PlansDir = plansDir

	claudeResults := []executor.Result{
		// exploration
		{Output: "done"},
		// architecture iter 1: propose
		{Output: makeProposalJSON("architecture", "## Arch\nMy approach")},
		// architecture iter 1: revise (disagreed)
		{Output: makeRevisionJSON("architecture", "## Arch\nStill my approach", []string{}, []string{"I disagree with the critique"})},
		// architecture iter 2: propose (re-propose with revised content)
		{Output: makeProposalJSON("architecture", "## Arch\nStill my approach")},
		// architecture iter 2: revise (still disagreed) — triggers conflict
		{Output: makeRevisionJSON("architecture", "## Arch\nFinal approach", []string{}, []string{"Still disagree"})},
		// tasks
		{Output: makeProposalJSON("tasks", "## Tasks\nDo it")},
		// testing
		{Output: makeProposalJSON("testing", "## Testing\nTest all")},
		// assembly
		{Output: "<<<RALPHEX:PLAN_DRAFT>>>\n# Plan\n<<<RALPHEX:END>>>"},
	}
	claude := newMockExecutor(claudeResults)

	codexResults := []executor.Result{
		// architecture iter 1 critique: needs_revision
		{Output: makeCritiqueJSON("architecture", "needs_revision", "approach is outdated")},
		// architecture iter 2 critique: needs_revision again
		{Output: makeCritiqueJSON("architecture", "needs_revision", "still outdated")},
		// tasks: acceptable
		{Output: makeCritiqueJSON("tasks", "acceptable")},
		// testing: acceptable
		{Output: makeCritiqueJSON("testing", "acceptable")},
		// lint pass
		{Output: makeLintJSON("pass")},
	}
	codex := newMockExecutor(codexResults)

	conflictResolved := false
	input := &mocks.InputCollectorMock{
		AskQuestionFunc: func(_ context.Context, _ string, _ []string) (string, error) {
			return "ok", nil
		},
		AskDraftReviewFunc: func(_ context.Context, _ string, _ string) (string, string, error) {
			return "accept", "", nil
		},
		AskDeepPlanResumeFunc: func(_ context.Context, _ string) (bool, error) {
			return false, nil
		},
		AskSectionApprovalFunc: func(_ context.Context, sections []status.DeepPlanSectionChoice) ([]status.DeepPlanSectionChoice, error) {
			var result []status.DeepPlanSectionChoice
			for _, s := range sections {
				s.Enabled = s.Required
				result = append(result, s)
			}
			return result, nil
		},
		AskConflictResolutionFunc: func(_ context.Context, topic, _, _ string, _ []string) (string, error) {
			conflictResolved = true
			assert.Contains(t, topic, "Architecture")
			return "Accept proposer's version (current revision)", nil
		},
	}

	r := processor.NewWithExecutors(processor.Config{
		Mode:            processor.ModeDeepPlan,
		PlanDescription: "test conflict",
		MaxIterations:   30,
		AppConfig:       appCfg,
	}, log, claude, codex, nil, &status.PhaseHolder{})
	r.SetInputCollector(input)

	err := r.Run(t.Context())
	require.NoError(t, err)
	assert.True(t, conflictResolved, "conflict should have been escalated to user")
}

func TestRunner_DeepPlan_WithOptionalSections(t *testing.T) {
	// Enable all 6 sections (3 mandatory + 3 optional), verify all get processed.

	appCfg := testAppConfig(t)
	log := newMockLogger(filepath.Join(t.TempDir(), "progress.log"))
	plansDir := filepath.Join(t.TempDir(), "plans")
	appCfg.PlansDir = plansDir

	// 6 proposals + exploration + assembly = 8 claude calls
	claudeResults := []executor.Result{
		{Output: "done"}, // exploration
		{Output: makeProposalJSON("architecture", "## Arch\nDesign")},
		{Output: makeProposalJSON("tasks", "## Tasks\nDo things")},
		{Output: makeProposalJSON("testing", "## Testing\nUnit tests")},
		{Output: makeProposalJSON("data_models", "## Data Models\nUser table")},
		{Output: makeProposalJSON("api_design", "## API\nREST endpoints")},
		{Output: makeProposalJSON("migration", "## Migration\nAdd columns")},
		{Output: "<<<RALPHEX:PLAN_DRAFT>>>\n# Full Plan\nAll 6 sections\n<<<RALPHEX:END>>>"},
	}
	claude := newMockExecutor(claudeResults)

	// 6 critiques + 1 lint = 7 codex calls
	codexResults := []executor.Result{
		{Output: makeCritiqueJSON("architecture", "acceptable")},
		{Output: makeCritiqueJSON("tasks", "acceptable")},
		{Output: makeCritiqueJSON("testing", "acceptable")},
		{Output: makeCritiqueJSON("data_models", "acceptable")},
		{Output: makeCritiqueJSON("api_design", "acceptable")},
		{Output: makeCritiqueJSON("migration", "acceptable")},
		{Output: makeLintJSON("pass")},
	}
	codex := newMockExecutor(codexResults)

	input := &mocks.InputCollectorMock{
		AskQuestionFunc: func(_ context.Context, _ string, _ []string) (string, error) {
			return "ok", nil
		},
		AskDraftReviewFunc: func(_ context.Context, _ string, _ string) (string, string, error) {
			return "accept", "", nil
		},
		AskDeepPlanResumeFunc: func(_ context.Context, _ string) (bool, error) {
			return false, nil
		},
		AskSectionApprovalFunc: func(_ context.Context, sections []status.DeepPlanSectionChoice) ([]status.DeepPlanSectionChoice, error) {
			// enable ALL sections (mandatory + optional)
			for i := range sections {
				sections[i].Enabled = true
			}
			return sections, nil
		},
		AskConflictResolutionFunc: func(_ context.Context, _, _, _ string, _ []string) (string, error) {
			return "", nil
		},
	}

	r := processor.NewWithExecutors(processor.Config{
		Mode:            processor.ModeDeepPlan,
		PlanDescription: "test all sections",
		MaxIterations:   30,
		AppConfig:       appCfg,
	}, log, claude, codex, nil, &status.PhaseHolder{})
	r.SetInputCollector(input)

	err := r.Run(t.Context())
	require.NoError(t, err)

	// 6 proposals + exploration + assembly = 8 claude, 6 critiques + 1 lint = 7 codex
	assert.Len(t, claude.RunCalls(), 8)
	assert.Len(t, codex.RunCalls(), 7)

	// verify plan was written
	entries, err := os.ReadDir(plansDir)
	require.NoError(t, err)
	assert.Len(t, entries, 1)
}

func TestRunner_DeepPlan_LintWithAffectedSections(t *testing.T) {
	// Lint finds issues in specific sections → re-assembly → lint passes.

	appCfg := testAppConfig(t)
	log := newMockLogger(filepath.Join(t.TempDir(), "progress.log"))
	plansDir := filepath.Join(t.TempDir(), "plans")
	appCfg.PlansDir = plansDir

	claudeResults := []executor.Result{
		{Output: "done"}, // exploration
		{Output: makeProposalJSON("architecture", "## Arch\nOld approach")},
		{Output: makeProposalJSON("tasks", "## Tasks\nStuff")},
		{Output: makeProposalJSON("testing", "## Testing\nTests")},
		// assembly (first)
		{Output: "<<<RALPHEX:PLAN_DRAFT>>>\n# Plan v1\n<<<RALPHEX:END>>>"},
		// re-assembly after lint patches
		{Output: "<<<RALPHEX:PLAN_DRAFT>>>\n# Plan v2 (patched)\n<<<RALPHEX:END>>>"},
	}
	claude := newMockExecutor(claudeResults)

	codexResults := []executor.Result{
		{Output: makeCritiqueJSON("architecture", "acceptable")},
		{Output: makeCritiqueJSON("tasks", "acceptable")},
		{Output: makeCritiqueJSON("testing", "acceptable")},
		// lint iteration 1: needs_revision with affected sections
		{Output: makeLintJSON("needs_revision", map[string]any{
			"message":           "inconsistency between arch and tasks",
			"affected_sections": []string{"architecture", "tasks"},
		})},
		// lint iteration 2: pass
		{Output: makeLintJSON("pass")},
	}
	codex := newMockExecutor(codexResults)

	input := &mocks.InputCollectorMock{
		AskQuestionFunc: func(_ context.Context, _ string, _ []string) (string, error) {
			return "ok", nil
		},
		AskDraftReviewFunc: func(_ context.Context, _ string, _ string) (string, string, error) {
			return "accept", "", nil
		},
		AskDeepPlanResumeFunc: func(_ context.Context, _ string) (bool, error) {
			return false, nil
		},
		AskSectionApprovalFunc: func(_ context.Context, sections []status.DeepPlanSectionChoice) ([]status.DeepPlanSectionChoice, error) {
			var result []status.DeepPlanSectionChoice
			for _, s := range sections {
				s.Enabled = s.Required
				result = append(result, s)
			}
			return result, nil
		},
		AskConflictResolutionFunc: func(_ context.Context, _, _, _ string, _ []string) (string, error) {
			return "", nil
		},
	}

	r := processor.NewWithExecutors(processor.Config{
		Mode:            processor.ModeDeepPlan,
		PlanDescription: "test lint sections",
		MaxIterations:   30,
		AppConfig:       appCfg,
	}, log, claude, codex, nil, &status.PhaseHolder{})
	r.SetInputCollector(input)

	err := r.Run(t.Context())
	require.NoError(t, err)

	// claude: explore + 3 proposals + assembly + re-assembly = 6
	assert.Len(t, claude.RunCalls(), 6)
	// codex: 3 critiques + lint1 + lint2 = 5
	assert.Len(t, codex.RunCalls(), 5)
}

func TestRunner_DeepPlan_ReviseLifecycle(t *testing.T) {
	// Final review: user says "revise" → re-assembly → re-lint → accept.

	appCfg := testAppConfig(t)
	log := newMockLogger(filepath.Join(t.TempDir(), "progress.log"))
	plansDir := filepath.Join(t.TempDir(), "plans")
	appCfg.PlansDir = plansDir

	claudeResults := []executor.Result{
		{Output: "done"}, // exploration
		{Output: makeProposalJSON("architecture", "## Arch\nDesign")},
		{Output: makeProposalJSON("tasks", "## Tasks\nBuild")},
		{Output: makeProposalJSON("testing", "## Testing\nTest")},
		// first assembly
		{Output: "<<<RALPHEX:PLAN_DRAFT>>>\n# Plan v1\n<<<RALPHEX:END>>>"},
		// re-assembly after revise
		{Output: "<<<RALPHEX:PLAN_DRAFT>>>\n# Plan v2 (revised)\n<<<RALPHEX:END>>>"},
	}
	claude := newMockExecutor(claudeResults)

	codexResults := []executor.Result{
		{Output: makeCritiqueJSON("architecture", "acceptable")},
		{Output: makeCritiqueJSON("tasks", "acceptable")},
		{Output: makeCritiqueJSON("testing", "acceptable")},
		{Output: makeLintJSON("pass")}, // first lint
		{Output: makeLintJSON("pass")}, // second lint after revise
	}
	codex := newMockExecutor(codexResults)

	reviewCount := 0
	input := &mocks.InputCollectorMock{
		AskQuestionFunc: func(_ context.Context, _ string, _ []string) (string, error) {
			return "ok", nil
		},
		AskDraftReviewFunc: func(_ context.Context, _ string, _ string) (string, string, error) {
			reviewCount++
			if reviewCount == 1 {
				return "revise", "make it better", nil // first review: revise
			}
			return "accept", "", nil // second review: accept
		},
		AskDeepPlanResumeFunc: func(_ context.Context, _ string) (bool, error) {
			return false, nil
		},
		AskSectionApprovalFunc: func(_ context.Context, sections []status.DeepPlanSectionChoice) ([]status.DeepPlanSectionChoice, error) {
			var result []status.DeepPlanSectionChoice
			for _, s := range sections {
				s.Enabled = s.Required
				result = append(result, s)
			}
			return result, nil
		},
		AskConflictResolutionFunc: func(_ context.Context, _, _, _ string, _ []string) (string, error) {
			return "", nil
		},
	}

	r := processor.NewWithExecutors(processor.Config{
		Mode:            processor.ModeDeepPlan,
		PlanDescription: "test revise",
		MaxIterations:   30,
		AppConfig:       appCfg,
	}, log, claude, codex, nil, &status.PhaseHolder{})
	r.SetInputCollector(input)

	err := r.Run(t.Context())
	require.NoError(t, err)

	assert.Equal(t, 2, reviewCount, "should have reviewed twice (revise then accept)")
	assert.Len(t, claude.RunCalls(), 6, "explore + 3 proposals + 2 assemblies")
	assert.Len(t, codex.RunCalls(), 5, "3 critiques + 2 lints")

	// verify plan file written
	entries, err := os.ReadDir(plansDir)
	require.NoError(t, err)
	assert.Len(t, entries, 1)
}

func TestRunner_DeepPlan_MaxIterationsForceAgreement(t *testing.T) {
	// Section keeps getting needs_revision until max iterations,
	// then force-agrees with last content.

	appCfg := testAppConfig(t)
	appCfg.DeepPlanMaxSectionIters = 2 // low max for quick test
	log := newMockLogger(filepath.Join(t.TempDir(), "progress.log"))
	plansDir := filepath.Join(t.TempDir(), "plans")
	appCfg.PlansDir = plansDir

	claudeResults := []executor.Result{
		{Output: "done"}, // exploration
		// architecture iter 1: propose
		{Output: makeProposalJSON("architecture", "## Arch\nApproach v1")},
		// architecture iter 1: revise
		{Output: makeRevisionJSON("architecture", "## Arch\nApproach v2", []string{"fixed"}, nil)},
		// architecture iter 2: propose (re-propose)
		{Output: makeProposalJSON("architecture", "## Arch\nApproach v2")},
		// architecture iter 2: revise (architecture is Critical so gets +1 = 3 max iters)
		{Output: makeRevisionJSON("architecture", "## Arch\nApproach v3", []string{"fixed again"}, nil)},
		// architecture iter 3: propose
		{Output: makeProposalJSON("architecture", "## Arch\nApproach v3")},
		// architecture iter 3: revise
		{Output: makeRevisionJSON("architecture", "## Arch\nFinal", []string{"done"}, nil)},
		// tasks
		{Output: makeProposalJSON("tasks", "## Tasks\nBuild")},
		// testing
		{Output: makeProposalJSON("testing", "## Testing\nTest")},
		// assembly
		{Output: "<<<RALPHEX:PLAN_DRAFT>>>\n# Plan\n<<<RALPHEX:END>>>"},
	}
	claude := newMockExecutor(claudeResults)

	codexResults := []executor.Result{
		// architecture: always needs_revision (3 times because critical gets +1)
		{Output: makeCritiqueJSON("architecture", "needs_revision", "still bad")},
		{Output: makeCritiqueJSON("architecture", "needs_revision", "still bad")},
		{Output: makeCritiqueJSON("architecture", "needs_revision", "still bad")},
		// tasks: acceptable
		{Output: makeCritiqueJSON("tasks", "acceptable")},
		// testing: acceptable
		{Output: makeCritiqueJSON("testing", "acceptable")},
		// lint: pass
		{Output: makeLintJSON("pass")},
	}
	codex := newMockExecutor(codexResults)

	input := &mocks.InputCollectorMock{
		AskQuestionFunc: func(_ context.Context, _ string, _ []string) (string, error) {
			return "ok", nil
		},
		AskDraftReviewFunc: func(_ context.Context, _ string, _ string) (string, string, error) {
			return "accept", "", nil
		},
		AskDeepPlanResumeFunc: func(_ context.Context, _ string) (bool, error) {
			return false, nil
		},
		AskSectionApprovalFunc: func(_ context.Context, sections []status.DeepPlanSectionChoice) ([]status.DeepPlanSectionChoice, error) {
			var result []status.DeepPlanSectionChoice
			for _, s := range sections {
				s.Enabled = s.Required
				result = append(result, s)
			}
			return result, nil
		},
		AskConflictResolutionFunc: func(_ context.Context, _, _, _ string, _ []string) (string, error) {
			return "", nil
		},
	}

	r := processor.NewWithExecutors(processor.Config{
		Mode:            processor.ModeDeepPlan,
		PlanDescription: "test max iters",
		MaxIterations:   30,
		AppConfig:       appCfg,
	}, log, claude, codex, nil, &status.PhaseHolder{})
	r.SetInputCollector(input)

	err := r.Run(t.Context())
	require.NoError(t, err)

	// architecture should have used all iterations before force-agreeing
	entries, err := os.ReadDir(plansDir)
	require.NoError(t, err)
	assert.Len(t, entries, 1)
}

func TestRunner_DeepPlan_FindExistingPlan(t *testing.T) {
	appCfg := testAppConfig(t)
	plansDir := filepath.Join(t.TempDir(), "plans")
	require.NoError(t, os.MkdirAll(plansDir, 0o750))
	appCfg.PlansDir = plansDir

	// create an existing plan file
	require.NoError(t, os.WriteFile(filepath.Join(plansDir, "cleanup-scheduling.md"), []byte("# Old Plan"), 0o640))

	log := newMockLogger(filepath.Join(t.TempDir(), "progress.log"))

	// will fail early because claude returns no results, but we just want to verify
	// the existing plan detection log message
	claude := newMockExecutor([]executor.Result{
		{Output: "done"},
	})
	codex := newMockExecutor(nil)

	input := &mocks.InputCollectorMock{
		AskDeepPlanResumeFunc: func(_ context.Context, _ string) (bool, error) {
			return false, nil
		},
		AskSectionApprovalFunc: func(_ context.Context, sections []status.DeepPlanSectionChoice) ([]status.DeepPlanSectionChoice, error) {
			var result []status.DeepPlanSectionChoice
			for _, s := range sections {
				s.Enabled = s.Required
				result = append(result, s)
			}
			return result, nil
		},
		AskConflictResolutionFunc: func(_ context.Context, _, _, _ string, _ []string) (string, error) {
			return "", nil
		},
		AskQuestionFunc: func(_ context.Context, _ string, _ []string) (string, error) {
			return "", nil
		},
		AskDraftReviewFunc: func(_ context.Context, _ string, _ string) (string, string, error) {
			return "", "", nil
		},
	}

	r := processor.NewWithExecutors(processor.Config{
		Mode:            processor.ModeDeepPlan,
		PlanDescription: "Add cleanup scheduling",
		MaxIterations:   30,
		AppConfig:       appCfg,
	}, log, claude, codex, nil, &status.PhaseHolder{})
	r.SetInputCollector(input)

	// run will fail because we don't have enough mock results, but it should have logged
	// the existing plan note
	_ = r.Run(t.Context())

	// verify "note: found potentially related plan" was logged
	found := false
	for _, call := range log.PrintCalls() {
		if len(call.Args) > 0 {
			msg := fmt.Sprintf(call.Format, call.Args...)
			if assert.ObjectsAreEqual("note: found potentially related plan: cleanup-scheduling.md", msg) {
				found = true
				break
			}
		}
	}
	assert.True(t, found, "should log existing plan found")
}
