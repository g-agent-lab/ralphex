package status

import "fmt"

// SectionType represents the semantic type of a section header.
// the web layer uses these types to emit appropriate boundary events:
//   - SectionTaskIteration: emits task_start/task_end events
//   - SectionClaudeReview, SectionCodexIteration: emits iteration_start events
//   - SectionGeneric, SectionClaudeEval: no boundary events, just section headers
//
// invariants:
//   - Iteration > 0 for SectionTaskIteration, SectionClaudeReview, SectionCodexIteration
//   - Iteration == 0 for SectionGeneric, SectionClaudeEval
//
// prefer using the constructor functions (NewTaskIterationSection, etc.) to ensure
// these invariants are maintained.
type SectionType int

const (
	// SectionGeneric is a static section header with no iteration.
	SectionGeneric SectionType = iota
	// SectionTaskIteration represents a task execution iteration.
	SectionTaskIteration
	// SectionClaudeReview represents a Claude review iteration.
	SectionClaudeReview
	// SectionCodexIteration represents a Codex review iteration.
	SectionCodexIteration
	// SectionClaudeEval represents Claude evaluating codex findings.
	SectionClaudeEval
	// SectionPlanIteration represents a plan creation iteration.
	SectionPlanIteration
	// SectionCustomIteration represents a custom review tool iteration.
	SectionCustomIteration
	// SectionDeepPlanExploration represents the exploration phase of deep plan.
	SectionDeepPlanExploration
	// SectionDeepPlanSectionApproval represents section list approval by user.
	SectionDeepPlanSectionApproval
	// SectionDeepPlanProposal represents a proposal iteration for a section.
	SectionDeepPlanProposal
	// SectionDeepPlanCritique represents a critique iteration for a section.
	SectionDeepPlanCritique
	// SectionDeepPlanRevision represents a revision iteration for a section.
	SectionDeepPlanRevision
	// SectionDeepPlanConflict represents a conflict requiring user arbitration.
	SectionDeepPlanConflict
	// SectionDeepPlanAssembly represents the assembly of all agreed sections.
	SectionDeepPlanAssembly
	// SectionDeepPlanLint represents the full-plan lint pass by reviewer.
	SectionDeepPlanLint
)

// Section carries structured information about a section header.
// instead of parsing section names with regex, consumers can access
// the Type and Iteration fields directly.
//
// use the provided constructors (NewTaskIterationSection, etc.) to create sections
// with proper Type/Iteration/Label consistency.
type Section struct {
	Type      SectionType
	Iteration int    // 0 for non-iterated sections
	Label     string // human-readable display text
}

// NewTaskIterationSection creates a section for task execution iteration.
func NewTaskIterationSection(iteration int) Section {
	return Section{
		Type:      SectionTaskIteration,
		Iteration: iteration,
		Label:     fmt.Sprintf("task iteration %d", iteration),
	}
}

// NewClaudeReviewSection creates a section for Claude review iteration.
// suffix is appended after the iteration number (e.g., ": critical/major").
func NewClaudeReviewSection(iteration int, suffix string) Section {
	return Section{
		Type:      SectionClaudeReview,
		Iteration: iteration,
		Label:     fmt.Sprintf("claude review %d%s", iteration, suffix),
	}
}

// NewCodexIterationSection creates a section for Codex review iteration.
func NewCodexIterationSection(iteration int) Section {
	return Section{
		Type:      SectionCodexIteration,
		Iteration: iteration,
		Label:     fmt.Sprintf("codex iteration %d", iteration),
	}
}

// NewClaudeEvalSection creates a section for Claude evaluating codex findings.
func NewClaudeEvalSection() Section {
	return Section{
		Type:  SectionClaudeEval,
		Label: "claude evaluating codex findings",
	}
}

// NewGenericSection creates a static section header with no iteration.
func NewGenericSection(label string) Section {
	return Section{
		Type:  SectionGeneric,
		Label: label,
	}
}

// NewPlanIterationSection creates a section for plan creation iteration.
func NewPlanIterationSection(iteration int) Section {
	return Section{
		Type:      SectionPlanIteration,
		Iteration: iteration,
		Label:     fmt.Sprintf("plan iteration %d", iteration),
	}
}

// NewCustomIterationSection creates a section for custom review tool iteration.
func NewCustomIterationSection(iteration int) Section {
	return Section{
		Type:      SectionCustomIteration,
		Iteration: iteration,
		Label:     fmt.Sprintf("custom review iteration %d", iteration),
	}
}

// NewDeepPlanExplorationSection creates a section for deep plan exploration.
func NewDeepPlanExplorationSection() Section {
	return Section{
		Type:  SectionDeepPlanExploration,
		Label: "deep plan: exploration",
	}
}

// NewDeepPlanSectionApprovalSection creates a section for section list approval.
func NewDeepPlanSectionApprovalSection() Section {
	return Section{
		Type:  SectionDeepPlanSectionApproval,
		Label: "deep plan: section approval",
	}
}

// NewDeepPlanProposalSection creates a section for a proposal iteration.
func NewDeepPlanProposalSection(sectionName string, iteration int) Section {
	return Section{
		Type:      SectionDeepPlanProposal,
		Iteration: iteration,
		Label:     fmt.Sprintf("deep plan: %s proposal %d", sectionName, iteration),
	}
}

// NewDeepPlanCritiqueSection creates a section for a critique iteration.
func NewDeepPlanCritiqueSection(sectionName string, iteration int) Section {
	return Section{
		Type:      SectionDeepPlanCritique,
		Iteration: iteration,
		Label:     fmt.Sprintf("deep plan: %s critique %d", sectionName, iteration),
	}
}

// NewDeepPlanRevisionSection creates a section for a revision iteration.
func NewDeepPlanRevisionSection(sectionName string, iteration int) Section {
	return Section{
		Type:      SectionDeepPlanRevision,
		Iteration: iteration,
		Label:     fmt.Sprintf("deep plan: %s revision %d", sectionName, iteration),
	}
}

// NewDeepPlanConflictSection creates a section for a conflict requiring arbitration.
func NewDeepPlanConflictSection(sectionName string) Section {
	return Section{
		Type:  SectionDeepPlanConflict,
		Label: fmt.Sprintf("deep plan: %s conflict", sectionName),
	}
}

// NewDeepPlanAssemblySection creates a section for plan assembly.
func NewDeepPlanAssemblySection() Section {
	return Section{
		Type:  SectionDeepPlanAssembly,
		Label: "deep plan: assembly",
	}
}

// NewDeepPlanLintSection creates a section for full-plan lint.
func NewDeepPlanLintSection(iteration int) Section {
	return Section{
		Type:      SectionDeepPlanLint,
		Iteration: iteration,
		Label:     fmt.Sprintf("deep plan: lint %d", iteration),
	}
}
