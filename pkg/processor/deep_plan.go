package processor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/umputun/ralphex/pkg/executor"
	"github.com/umputun/ralphex/pkg/status"
)

const (
	deepPlanMaxParseRetries = 2 // retry JSON parse failures up to 2 times
	deepPlanMaxLintIters    = 2 // max lint → patch → re-lint iterations
)

// ModeDeepPlan is the adversarial deep plan creation mode.
const ModeDeepPlan Mode = "deep-plan"

// runDeepPlanCreation orchestrates the full adversarial deep plan pipeline.
// phases: exploration → section approval → section loop → assembly → lint → final review.
func (r *Runner) runDeepPlanCreation(ctx context.Context) error {
	if r.cfg.PlanDescription == "" {
		return errors.New("plan description required for deep plan mode")
	}
	if r.inputCollector == nil {
		return errors.New("input collector required for deep plan mode")
	}

	r.phaseHolder.Set(status.PhaseDeepPlan)
	r.log.PrintRaw("starting adversarial deep plan creation\n")
	r.log.Print("plan request: %s", r.cfg.PlanDescription)

	// check for existing plan in plans dir (deterministic Go check, not LLM)
	if existing := r.findExistingPlan(); existing != "" {
		r.log.Print("note: found potentially related plan: %s", existing)
	}

	// check for resume state
	progress, err := r.loadOrCreateDeepPlanProgress(ctx)
	if err != nil {
		return err
	}

	// phase 0: exploration + section determination
	if progress.Phase == "exploration" {
		if err := r.runDeepPlanExploration(ctx, progress); err != nil {
			return fmt.Errorf("exploration: %w", err)
		}
	}

	// section loop
	if progress.Phase == "section_loop" {
		if err := r.runDeepPlanSectionLoopAll(ctx, progress); err != nil {
			return fmt.Errorf("section loop: %w", err)
		}
	}

	// assembly
	if progress.Phase == "assembly" {
		if err := r.runDeepPlanAssembly(ctx, progress); err != nil {
			return fmt.Errorf("assembly: %w", err)
		}
	}

	// lint
	if progress.Phase == "lint" {
		if err := r.runDeepPlanLint(ctx, progress); err != nil {
			return fmt.Errorf("lint: %w", err)
		}
	}

	// final review
	if progress.Phase == "final_review" {
		if err := r.runDeepPlanFinalReview(ctx, progress); err != nil {
			return fmt.Errorf("final review: %w", err)
		}
	}

	return nil
}

// loadOrCreateDeepPlanProgress loads existing state or creates new one.
// asks user to resume if state file exists.
func (r *Runner) loadOrCreateDeepPlanProgress(ctx context.Context) (*deepPlanProgress, error) {
	existing, err := loadDeepPlanState(r.cfg.PlanDescription)
	if err != nil {
		return nil, fmt.Errorf("load state: %w", err)
	}

	if existing != nil {
		// build state info for user
		agreed := 0
		total := len(existing.Sections)
		for _, s := range existing.Sections {
			if existing.States[s.Name] == deepPlanStateAgreed {
				agreed++
			}
		}
		stateInfo := fmt.Sprintf("phase: %s, %d of %d sections completed", existing.Phase, agreed, total)

		resume, askErr := r.inputCollector.AskDeepPlanResume(ctx, stateInfo)
		if askErr != nil {
			return nil, fmt.Errorf("ask resume: %w", askErr)
		}
		if resume {
			r.log.Print("resuming deep plan from saved state")
			return existing, nil
		}
		// start over — clean up old state
		if err := cleanupDeepPlanState(r.cfg.PlanDescription); err != nil {
			r.log.Print("warning: failed to clean up old state: %v", err)
		}
	}

	progress := newDeepPlanProgress(r.cfg.PlanDescription, "0.21.0")
	return progress, nil
}

// runDeepPlanExploration runs the exploration phase (Claude explores codebase, asks questions).
// when done, asks user to approve sections, then transitions to section_loop.
func (r *Runner) runDeepPlanExploration(ctx context.Context, progress *deepPlanProgress) error {
	r.log.PrintSection(status.NewGenericSection("deep plan: exploration"))

	maxIters := max(minPlanIterations, r.cfg.MaxIterations/planIterationDivisor)

	for i := 1; i <= maxIters; i++ {
		select {
		case <-ctx.Done():
			return fmt.Errorf("exploration: %w", ctx.Err())
		default:
		}

		prompt := r.buildDeepPlanExplorePrompt()
		result := r.runWithLimitRetry(ctx, r.claude.Run, prompt, "claude")
		if result.Error != nil {
			if err := r.handlePatternMatchError(result.Error, "claude"); err != nil {
				return err
			}
			return fmt.Errorf("claude execution: %w", result.Error)
		}

		if result.Signal == SignalFailed {
			return errors.New("exploration failed (FAILED signal received)")
		}

		// check for QUESTION signal
		handled, err := r.handlePlanQuestion(ctx, result.Output)
		if err != nil {
			return err
		}
		if handled {
			if err := r.sleepWithContext(ctx, r.iterationDelay); err != nil {
				return fmt.Errorf("interrupted: %w", err)
			}
			continue
		}

		// exploration done — move to section approval
		break
	}

	// section approval
	sections := r.buildSectionChoices()
	approved, err := r.inputCollector.AskSectionApproval(ctx, sections)
	if err != nil {
		return fmt.Errorf("section approval: %w", err)
	}

	// convert approved choices back to deepPlanSection
	var enabledSects []deepPlanSection
	for _, choice := range approved {
		if !choice.Enabled {
			continue
		}
		for _, s := range allSections {
			if s.Name == choice.Name {
				sect := s
				sect.Enabled = true
				enabledSects = append(enabledSects, sect)
				break
			}
		}
	}

	if err := validateSectionInvariants(enabledSects); err != nil {
		return fmt.Errorf("section invariants: %w", err)
	}

	// update progress
	progress.Sections = enabledSects
	for _, s := range enabledSects {
		progress.States[s.Name] = deepPlanStatePending
	}
	progress.Phase = "section_loop"
	progress.CurrentSection = 0
	if err := saveDeepPlanState(progress); err != nil {
		return fmt.Errorf("save state: %w", err)
	}

	r.log.Print("sections approved: %d", len(enabledSects))
	return nil
}

// buildSectionChoices converts allSections to DeepPlanSectionChoice for user approval.
func (r *Runner) buildSectionChoices() []status.DeepPlanSectionChoice {
	var choices []status.DeepPlanSectionChoice
	for _, s := range allSections {
		choices = append(choices, status.DeepPlanSectionChoice{
			Name:        s.Name,
			DisplayName: s.DisplayName,
			Required:    s.Required,
			Enabled:     true, // all enabled by default
		})
	}
	return choices
}

// runDeepPlanSectionLoopAll iterates over all enabled sections.
func (r *Runner) runDeepPlanSectionLoopAll(ctx context.Context, progress *deepPlanProgress) error {
	enabled := enabledSections(progress.Sections)

	for i := progress.CurrentSection; i < len(enabled); i++ {
		select {
		case <-ctx.Done():
			return fmt.Errorf("section loop: %w", ctx.Err())
		default:
		}

		section := enabled[i]
		if progress.States[section.Name] == deepPlanStateAgreed {
			continue // already agreed (resume case)
		}

		r.log.PrintSection(status.NewGenericSection(
			fmt.Sprintf("deep plan: section %d/%d — %s", i+1, len(enabled), section.DisplayName)))

		if err := r.runDeepPlanSectionLoop(ctx, progress, section); err != nil {
			return fmt.Errorf("section %s: %w", section.Name, err)
		}

		progress.CurrentSection = i + 1
		if err := saveDeepPlanState(progress); err != nil {
			return fmt.Errorf("save state: %w", err)
		}
	}

	// transition to assembly
	progress.Phase = "assembly"
	if err := saveDeepPlanState(progress); err != nil {
		return fmt.Errorf("save state: %w", err)
	}
	return nil
}

// runDeepPlanSectionLoop runs the adversarial loop for a single section.
// propose → critique → [revise → re-critique] → agree/conflict
func (r *Runner) runDeepPlanSectionLoop(ctx context.Context, progress *deepPlanProgress, section deepPlanSection) error {
	maxIters := r.deepPlanMaxSectionIters()
	if section.Critical {
		maxIters++ // critical sections get +1 iteration
	}

	agreedSections := r.buildAgreedSectionsContext(progress)

	for iter := 1; iter <= maxIters; iter++ {
		select {
		case <-ctx.Done():
			return fmt.Errorf("section loop: %w", ctx.Err())
		default:
		}

		progress.Iterations[section.Name] = iter

		// step 1: Claude proposes
		r.log.Print("  [%d/%d] proposing...", iter, maxIters)
		var reviewerFeedback string
		if iter > 1 {
			// include previous critique feedback for context
			reviewerFeedback = r.getLastCritiqueSummary(progress, section.Name)
		}

		proposal, err := r.deepPlanPropose(ctx, section, agreedSections, reviewerFeedback)
		if err != nil {
			return fmt.Errorf("propose: %w", err)
		}
		progress.States[section.Name] = deepPlanStateProposed

		// step 2: Codex critiques
		r.log.Print("  [%d/%d] critiquing...", iter, maxIters)
		critique, err := r.deepPlanCritique(ctx, section, agreedSections, proposal.Content)
		if err != nil {
			return fmt.Errorf("critique: %w", err)
		}

		// acceptable → agreed
		if critique.Verdict == "acceptable" {
			r.log.Print("  section %s agreed (iter %d)", section.Name, iter)
			progress.States[section.Name] = deepPlanStateAgreed
			progress.AgreedContent[section.Name] = proposal.Content
			return nil
		}

		// needs_revision → revise
		r.log.Print("  [%d/%d] revising (%d issues)...", iter, maxIters, len(critique.Issues))
		progress.States[section.Name] = deepPlanStateRevising

		// marshal critique issues for revision prompt
		critiqueJSON, _ := json.Marshal(critique.Issues)

		revision, err := r.deepPlanRevise(ctx, section, agreedSections, proposal.Content, string(critiqueJSON))
		if err != nil {
			return fmt.Errorf("revise: %w", err)
		}

		// check for disagreements after 2+ iterations → conflict escalation
		if len(revision.Disagreed) > 0 && iter >= 2 {
			r.log.Print("  persistent disagreement on %s, escalating to user", section.Name)
			resolved, resolveErr := r.handleDeepPlanConflict(ctx, progress, section, revision)
			if resolveErr != nil {
				return fmt.Errorf("conflict: %w", resolveErr)
			}
			if resolved {
				return nil
			}
			// not resolved — continue iterating with updated content
		}

		// use revised content as the new proposal for next iteration
		progress.AgreedContent[section.Name] = revision.Content
		// store critique summary for next iteration context
		progress.Decisions[section.Name+"/last_critique"] = string(critiqueJSON)

		if err := r.sleepWithContext(ctx, r.iterationDelay); err != nil {
			return fmt.Errorf("interrupted: %w", err)
		}
	}

	// max iterations reached — force agreement with last content
	r.log.Print("  max iterations reached for %s, using last revision", section.Name)
	progress.States[section.Name] = deepPlanStateAgreed
	if progress.AgreedContent[section.Name] == "" {
		return fmt.Errorf("section %s: no content after max iterations", section.Name)
	}
	return nil
}

// deepPlanPropose runs Claude to propose a section and parses the structured JSON output.
func (r *Runner) deepPlanPropose(ctx context.Context, section deepPlanSection, agreedSections, reviewerFeedback string) (*deepPlanProposalPayload, error) {
	prompt := r.buildDeepPlanProposePrompt(section.Name, section.DisplayName, agreedSections, reviewerFeedback)
	return parseWithRetry[deepPlanProposalPayload](ctx, r, r.claude, prompt, "claude", deepPlanMaxParseRetries)
}

// deepPlanCritique runs Codex to critique a proposed section.
func (r *Runner) deepPlanCritique(ctx context.Context, section deepPlanSection, agreedSections, proposalContent string) (*deepPlanCritiquePayload, error) {
	prompt := r.buildDeepPlanCritiquePrompt(section.Name, section.DisplayName, agreedSections, proposalContent)
	return parseWithRetry[deepPlanCritiquePayload](ctx, r, r.codex, prompt, "codex", deepPlanMaxParseRetries)
}

// deepPlanRevise runs Claude to revise a section after critique.
func (r *Runner) deepPlanRevise(ctx context.Context, section deepPlanSection, agreedSections, proposalContent, critiqueIssues string) (*deepPlanRevisionPayload, error) {
	prompt := r.buildDeepPlanResolvePrompt(section.Name, section.DisplayName, agreedSections, proposalContent, critiqueIssues)
	return parseWithRetry[deepPlanRevisionPayload](ctx, r, r.claude, prompt, "claude", deepPlanMaxParseRetries)
}

// parseWithRetry runs an executor and parses structured JSON from its output.
// retries up to maxRetries times on parse failures (not executor failures).
func parseWithRetry[T any, PT interface {
	*T
	jsonValidator
}](ctx context.Context, r *Runner, exec Executor, prompt, toolName string, maxRetries int) (*T, error) {
	var lastParseErr error

	for attempt := 0; attempt <= maxRetries; attempt++ {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("parse with retry: %w", ctx.Err())
		default:
		}

		if attempt > 0 {
			r.log.Print("  retrying %s (parse attempt %d/%d)...", toolName, attempt+1, maxRetries+1)
			// append parse error context to prompt for retry
			retryPrompt := fmt.Sprintf("%s\n\n---\nPREVIOUS ATTEMPT FAILED TO PARSE:\n%s\n\nPlease output ONLY a valid JSON object.", prompt, lastParseErr)
			prompt = retryPrompt
		}

		result := r.runWithLimitRetry(ctx, exec.Run, prompt, toolName)
		if result.Error != nil {
			if err := r.handlePatternMatchError(result.Error, toolName); err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("%s execution: %w", toolName, result.Error)
		}

		if result.Signal == SignalFailed {
			return nil, fmt.Errorf("%s reported failure (FAILED signal)", toolName)
		}

		payload, parseErr := parseStructuredJSON[T, PT](result.Output)
		if parseErr != nil {
			lastParseErr = parseErr
			r.log.Print("  parse error: %v", parseErr)
			continue
		}

		return payload, nil
	}

	return nil, fmt.Errorf("failed to parse %s output after %d attempts: %w", toolName, maxRetries+1, lastParseErr)
}

// handleDeepPlanConflict escalates a persistent disagreement to the user for arbitration.
// returns true if user resolved the conflict and section is agreed.
func (r *Runner) handleDeepPlanConflict(ctx context.Context, progress *deepPlanProgress, section deepPlanSection, revision *deepPlanRevisionPayload) (bool, error) {
	progress.States[section.Name] = deepPlanStateConflict

	// build conflict description from disagreements
	topic := fmt.Sprintf("Disagreement on %s", section.DisplayName)
	proposerArg := strings.Join(revision.Disagreed, "\n")
	reviewerArg := "See critique issues above"

	options := []string{
		"Accept proposer's version (current revision)",
		"Request another iteration",
	}

	choice, err := r.inputCollector.AskConflictResolution(ctx, topic, proposerArg, reviewerArg, options)
	if err != nil {
		return false, fmt.Errorf("conflict resolution: %w", err)
	}

	// record decision
	progress.Decisions[section.Name+"/conflict"] = choice

	if err := saveDeepPlanState(progress); err != nil {
		return false, fmt.Errorf("save state: %w", err)
	}

	if strings.Contains(choice, "Accept proposer") {
		progress.States[section.Name] = deepPlanStateAgreed
		progress.AgreedContent[section.Name] = revision.Content
		r.log.Print("  conflict resolved: user accepted proposer's version")
		return true, nil
	}

	// user wants another iteration
	r.log.Print("  conflict: user requested another iteration")
	return false, nil
}

// buildAgreedSectionsContext builds a combined text of all previously agreed sections.
func (r *Runner) buildAgreedSectionsContext(progress *deepPlanProgress) string {
	var parts []string
	for _, s := range progress.Sections {
		if content, ok := progress.AgreedContent[s.Name]; ok && content != "" {
			parts = append(parts, content)
		}
	}
	if len(parts) == 0 {
		return "(no sections agreed yet)"
	}
	return strings.Join(parts, "\n\n---\n\n")
}

// getLastCritiqueSummary returns the stored critique summary for a section if available.
func (r *Runner) getLastCritiqueSummary(progress *deepPlanProgress, sectionName string) string {
	if critique, ok := progress.Decisions[sectionName+"/last_critique"]; ok {
		return critique
	}
	return ""
}

// deepPlanMaxSectionIters returns the maximum section iterations from config or default.
func (r *Runner) deepPlanMaxSectionIters() int {
	if r.cfg.AppConfig != nil && r.cfg.AppConfig.DeepPlanMaxSectionIters > 0 {
		return r.cfg.AppConfig.DeepPlanMaxSectionIters
	}
	return 3 // default
}

// runDeepPlanAssembly runs Claude to assemble all agreed sections into a plan.
func (r *Runner) runDeepPlanAssembly(ctx context.Context, progress *deepPlanProgress) error {
	r.log.PrintSection(status.NewGenericSection("deep plan: assembly"))

	agreedSections := r.buildAgreedSectionsContext(progress)
	architectureDecisions := r.buildArchitectureDecisions(progress)

	prompt := r.buildDeepPlanAssemblyPrompt(agreedSections, architectureDecisions)
	result := r.runWithLimitRetry(ctx, r.claude.Run, prompt, "claude")
	if result.Error != nil {
		if err := r.handlePatternMatchError(result.Error, "claude"); err != nil {
			return err
		}
		return fmt.Errorf("claude execution: %w", result.Error)
	}

	if result.Signal == SignalFailed {
		return errors.New("assembly failed (FAILED signal received)")
	}

	// extract PLAN_DRAFT from output
	planContent, err := ParsePlanDraftPayload(result.Output)
	if err != nil {
		return fmt.Errorf("assembly: %w", err)
	}

	progress.OutputPlanPath = planContent
	progress.Phase = "lint"
	if err := saveDeepPlanState(progress); err != nil {
		return fmt.Errorf("save state: %w", err)
	}

	r.log.Print("assembly completed, proceeding to lint")
	return nil
}

// buildArchitectureDecisions builds the architecture decisions context from progress.
func (r *Runner) buildArchitectureDecisions(progress *deepPlanProgress) string {
	var decisions []string
	for key, value := range progress.Decisions {
		if strings.Contains(key, "/conflict") {
			decisions = append(decisions, fmt.Sprintf("- %s: %s", key, value))
		}
	}
	if len(decisions) == 0 {
		return "(no architecture decisions recorded)"
	}
	return strings.Join(decisions, "\n")
}

// runDeepPlanLint runs Codex to review the assembled plan and patch any issues.
func (r *Runner) runDeepPlanLint(ctx context.Context, progress *deepPlanProgress) error {
	r.log.PrintSection(status.NewGenericSection("deep plan: lint"))

	for lintIter := 1; lintIter <= deepPlanMaxLintIters; lintIter++ {
		select {
		case <-ctx.Done():
			return fmt.Errorf("lint: %w", ctx.Err())
		default:
		}

		r.log.Print("  lint iteration %d/%d", lintIter, deepPlanMaxLintIters)

		prompt := r.buildDeepPlanLintPrompt(progress.OutputPlanPath)
		lint, err := parseWithRetry[deepPlanLintPayload](ctx, r, r.codex, prompt, "codex", deepPlanMaxParseRetries)
		if err != nil {
			return fmt.Errorf("lint: %w", err)
		}

		if lint.Verdict == "pass" {
			r.log.Print("  lint passed")
			break
		}

		// patch affected sections
		r.log.Print("  lint found %d issues, patching...", len(lint.Issues))
		for _, issue := range lint.Issues {
			for _, sectionName := range issue.AffectedSections {
				if _, ok := progress.AgreedContent[sectionName]; !ok {
					r.log.Print("  warning: lint references unknown section %q, skipping", sectionName)
					continue
				}
				progress.States[sectionName] = deepPlanStatePatching
				r.log.Print("  patching section %s: %s", sectionName, truncate(issue.Message, 80))
			}
		}

		// re-assemble from patched agreed content
		if err := r.runDeepPlanAssembly(ctx, progress); err != nil {
			return fmt.Errorf("re-assembly: %w", err)
		}
		// assembly sets phase to "lint", so we continue the loop
	}

	progress.Phase = "final_review"
	if err := saveDeepPlanState(progress); err != nil {
		return fmt.Errorf("save state: %w", err)
	}
	return nil
}

// runDeepPlanFinalReview presents the assembled plan for user review.
// accept → write file + cleanup | revise → re-assembly + re-lint | reject → fail + cleanup
func (r *Runner) runDeepPlanFinalReview(ctx context.Context, progress *deepPlanProgress) error {
	r.log.PrintSection(status.NewGenericSection("deep plan: final review"))

	action, feedback, err := r.inputCollector.AskDraftReview(ctx, "Review the deep plan", progress.OutputPlanPath)
	if err != nil {
		return fmt.Errorf("final review: %w", err)
	}

	r.log.LogDraftReview(action, feedback)

	switch action {
	case "accept":
		return r.writeDeepPlanFile(progress)
	case "revise":
		r.log.Print("revision requested, re-running assembly...")
		progress.Phase = "assembly"
		if err := saveDeepPlanState(progress); err != nil {
			return fmt.Errorf("save state: %w", err)
		}
		// recurse through assembly → lint → final review
		if err := r.runDeepPlanAssembly(ctx, progress); err != nil {
			return fmt.Errorf("re-assembly: %w", err)
		}
		if err := r.runDeepPlanLint(ctx, progress); err != nil {
			return fmt.Errorf("re-lint: %w", err)
		}
		return r.runDeepPlanFinalReview(ctx, progress)
	case "reject":
		_ = cleanupDeepPlanState(r.cfg.PlanDescription)
		return ErrUserRejectedPlan
	}

	return fmt.Errorf("unexpected review action: %s", action)
}

// writeDeepPlanFile writes the assembled plan to the plans directory.
func (r *Runner) writeDeepPlanFile(progress *deepPlanProgress) error {
	plansDir := r.getPlansDir()
	if err := os.MkdirAll(plansDir, 0o750); err != nil {
		return fmt.Errorf("create plans dir: %w", err)
	}

	// generate filename from description
	filename := sanitizeFilename(r.cfg.PlanDescription) + ".md"
	path := filepath.Join(plansDir, filename)

	if err := os.WriteFile(path, []byte(progress.OutputPlanPath), 0o640); err != nil {
		return fmt.Errorf("write plan file: %w", err)
	}

	r.log.Print("plan written to %s", path)

	// cleanup state file on success
	if err := cleanupDeepPlanState(r.cfg.PlanDescription); err != nil {
		r.log.Print("warning: failed to clean up state: %v", err)
	}

	return nil
}

// findExistingPlan scans the plans directory for a potentially related plan file.
// returns the filename if found, empty string otherwise.
func (r *Runner) findExistingPlan() string {
	plansDir := r.getPlansDir()
	entries, err := os.ReadDir(plansDir)
	if err != nil {
		return ""
	}

	desc := strings.ToLower(r.cfg.PlanDescription)
	words := strings.Fields(desc)
	if len(words) == 0 {
		return ""
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		name := strings.ToLower(entry.Name())
		// simple heuristic: check if any significant word from description appears in filename
		for _, word := range words {
			if len(word) > 3 && strings.Contains(name, word) {
				return entry.Name()
			}
		}
	}
	return ""
}

// sanitizeFilename converts a description to a safe filename.
func sanitizeFilename(desc string) string {
	// lowercase and replace spaces/special chars with hyphens
	result := strings.ToLower(desc)
	var b strings.Builder
	lastHyphen := false
	for _, ch := range result {
		if ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' {
			b.WriteRune(ch)
			lastHyphen = false
		} else if !lastHyphen {
			b.WriteByte('-')
			lastHyphen = true
		}
	}
	s := strings.Trim(b.String(), "-")
	if len(s) > 60 {
		s = s[:60]
	}
	return s
}

// truncate returns at most n characters from s, appending "..." if truncated.
func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "..."
}

// runWithExecutor is a convenience wrapper for running a prompt with a specific executor and handling errors.
func (r *Runner) runWithExecutor(ctx context.Context, exec Executor, prompt, toolName string) (executor.Result, error) {
	result := r.runWithLimitRetry(ctx, exec.Run, prompt, toolName)
	if result.Error != nil {
		if err := r.handlePatternMatchError(result.Error, toolName); err != nil {
			return result, err
		}
		return result, fmt.Errorf("%s execution: %w", toolName, result.Error)
	}
	return result, nil
}
