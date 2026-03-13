// Package status defines shared execution-model types for ralphex.
// signal constants, phase types, and section types used by processor, executor, progress, and web packages.
package status

// signal constants using <<<RALPHEX:...>>> format for clear detection.
const (
	Completed        = "<<<RALPHEX:ALL_TASKS_DONE>>>"
	Failed           = "<<<RALPHEX:TASK_FAILED>>>"
	ReviewDone       = "<<<RALPHEX:REVIEW_DONE>>>"
	CodexDone        = "<<<RALPHEX:CODEX_REVIEW_DONE>>>"
	Question         = "<<<RALPHEX:QUESTION>>>"
	PlanReady        = "<<<RALPHEX:PLAN_READY>>>"
	PlanDraft        = "<<<RALPHEX:PLAN_DRAFT>>>"
	DeepPlanConflict = "<<<RALPHEX:DEEP_PLAN_CONFLICT>>>"
)

// Phase represents execution phase for color coding.
type Phase string

// Phase constants for execution stages.
const (
	PhaseTask       Phase = "task"        // execution phase (green)
	PhaseReview     Phase = "review"      // code review phase (cyan)
	PhaseCodex      Phase = "codex"       // codex analysis phase (magenta)
	PhaseClaudeEval Phase = "claude-eval" // claude evaluating codex (bright cyan)
	PhasePlan       Phase = "plan"        // plan creation phase (info color)
	PhaseDeepPlan   Phase = "deep-plan"   // adversarial deep plan creation phase
	PhaseFinalize   Phase = "finalize"    // finalize step phase (green)
)

// DeepPlanSectionChoice is a lightweight section descriptor for the user approval UI.
// defined in the shared status package so both processor and input packages can reference it
// without creating circular dependencies.
type DeepPlanSectionChoice struct {
	Name        string // stable section identifier (e.g., "architecture")
	DisplayName string // human-readable name (e.g., "Architecture & Approach")
	Required    bool   // mandatory sections cannot be disabled
	Enabled     bool   // whether this section is included in the plan
}
