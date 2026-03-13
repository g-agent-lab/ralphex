package processor

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// deep plan section states
const (
	deepPlanStatePending   = "pending"
	deepPlanStateProposed  = "proposed"
	deepPlanStateRevising  = "revising"
	deepPlanStateConflict  = "conflict"
	deepPlanStateAgreed    = "agreed"
	deepPlanStatePatching  = "patching" // lint-triggered patch in progress
)

// required sections that cannot be removed from deep plan
var requiredSections = map[string]bool{
	"architecture": true,
	"tasks":        true,
	"testing":      true,
}

// allSections is the fixed set of sections for deep plan v1.
// mandatory sections appear first; optional sections follow.
var allSections = []deepPlanSection{
	{Name: "architecture", DisplayName: "Architecture & Approach", Required: true, Critical: true},
	{Name: "tasks", DisplayName: "Implementation Tasks", Required: true},
	{Name: "testing", DisplayName: "Testing Strategy", Required: true},
	{Name: "data_models", DisplayName: "Data Models & Schemas"},
	{Name: "api_design", DisplayName: "API Design"},
	{Name: "migration", DisplayName: "Migration Strategy"},
}

// deepPlanSection describes a section of the deep plan.
type deepPlanSection struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Required    bool   `json:"required"`
	Critical    bool   `json:"critical,omitempty"` // critical sections get +1 iteration
	Enabled     bool   `json:"enabled"`
}

// deepPlanProgress holds the full state of a deep plan creation session.
type deepPlanProgress struct {
	Description    string                    `json:"description"`
	OutputPlanPath string                    `json:"output_plan_path"`
	CreatedAt      string                    `json:"created_at"`
	ToolVersion    string                    `json:"tool_version"`
	Phase          string                    `json:"phase"` // exploration, section_loop, assembly, lint, final_review
	Sections       []deepPlanSection         `json:"sections"`
	States         map[string]string         `json:"states"`         // section name → state
	AgreedContent  map[string]string         `json:"agreed_content"` // section name → agreed markdown content
	Decisions      map[string]string         `json:"decisions"`      // "section/topic" → decision text
	CurrentSection int                       `json:"current_section"`
	Iterations     map[string]int            `json:"iterations"` // section name → current iteration count
}

// --- payload types for structured JSON from LLM output ---

// deepPlanProposalPayload is the expected JSON from Claude's proposal.
type deepPlanProposalPayload struct {
	SchemaVersion int    `json:"schema_version"`
	Section       string `json:"section"`
	Content       string `json:"content"`
}

func (p *deepPlanProposalPayload) validate() error {
	if p.SchemaVersion != 1 {
		return fmt.Errorf("unsupported schema_version: %d", p.SchemaVersion)
	}
	if p.Section == "" {
		return errors.New("missing section field")
	}
	if p.Content == "" {
		return errors.New("missing content field")
	}
	return nil
}

// deepPlanCritiquePayload is the expected JSON from Codex's critique.
type deepPlanCritiquePayload struct {
	SchemaVersion int                    `json:"schema_version"`
	Section       string                 `json:"section"`
	Issues        []deepPlanCritiqueItem `json:"issues"`
	Verdict       string                 `json:"verdict"` // "acceptable" or "needs_revision"
}

type deepPlanCritiqueItem struct {
	Message  string `json:"message"`
	Severity string `json:"severity,omitempty"` // "critical", "major", "minor"
}

func (p *deepPlanCritiquePayload) validate() error {
	if p.SchemaVersion != 1 {
		return fmt.Errorf("unsupported schema_version: %d", p.SchemaVersion)
	}
	if p.Section == "" {
		return errors.New("missing section field")
	}
	if p.Verdict == "" {
		return errors.New("missing verdict field")
	}
	if p.Verdict != "acceptable" && p.Verdict != "needs_revision" {
		return fmt.Errorf("invalid verdict: %q (expected acceptable or needs_revision)", p.Verdict)
	}
	return nil
}

// deepPlanRevisionPayload is the expected JSON from Claude's revision.
type deepPlanRevisionPayload struct {
	SchemaVersion int      `json:"schema_version"`
	Section       string   `json:"section"`
	Content       string   `json:"content"`
	Addressed     []string `json:"addressed"`
	Disagreed     []string `json:"disagreed"`
}

func (p *deepPlanRevisionPayload) validate() error {
	if p.SchemaVersion != 1 {
		return fmt.Errorf("unsupported schema_version: %d", p.SchemaVersion)
	}
	if p.Section == "" {
		return errors.New("missing section field")
	}
	if p.Content == "" {
		return errors.New("missing content field")
	}
	return nil
}

// deepPlanLintPayload is the expected JSON from Codex's full-plan lint.
type deepPlanLintPayload struct {
	SchemaVersion int                  `json:"schema_version"`
	Issues        []deepPlanLintIssue  `json:"issues"`
	Verdict       string               `json:"verdict"` // "pass" or "needs_revision"
}

type deepPlanLintIssue struct {
	Message          string   `json:"message"`
	AffectedSections []string `json:"affected_sections"`
}

func (p *deepPlanLintPayload) validate() error {
	if p.SchemaVersion != 1 {
		return fmt.Errorf("unsupported schema_version: %d", p.SchemaVersion)
	}
	if p.Verdict == "" {
		return errors.New("missing verdict field")
	}
	if p.Verdict != "pass" && p.Verdict != "needs_revision" {
		return fmt.Errorf("invalid verdict: %q (expected pass or needs_revision)", p.Verdict)
	}
	for i, issue := range p.Issues {
		if issue.Message == "" {
			return fmt.Errorf("issue %d: missing message", i)
		}
		if len(issue.AffectedSections) == 0 {
			return fmt.Errorf("issue %d: missing affected_sections", i)
		}
	}
	return nil
}

// --- balanced-brace JSON extractor ---

// extractJSON extracts exactly one top-level JSON object from the given text
// using a balanced-brace scanner. Returns error if zero or multiple objects found.
func extractJSON(text string) (string, error) {
	var results []string
	depth := 0
	inString := false
	escaped := false
	start := -1

	for i, ch := range text {
		if escaped {
			escaped = false
			continue
		}

		if ch == '\\' && inString {
			escaped = true
			continue
		}

		if ch == '"' {
			inString = !inString
			continue
		}

		if inString {
			continue
		}

		switch ch {
		case '{':
			if depth == 0 {
				start = i
			}
			depth++
		case '}':
			depth--
			if depth == 0 && start >= 0 {
				results = append(results, text[start:i+1])
				start = -1
			}
			if depth < 0 {
				depth = 0 // recover from malformed input
			}
		}
	}

	if len(results) == 0 {
		return "", errors.New("no JSON object found in output")
	}
	if len(results) > 1 {
		return "", fmt.Errorf("expected exactly 1 JSON object, found %d", len(results))
	}
	return results[0], nil
}

// --- parseWithRetry: structured JSON parsing with retry ---

// jsonValidator is a function that validates a parsed payload.
type jsonValidator interface {
	validate() error
}

// parseStructuredJSON extracts JSON from LLM output using balanced-brace scanner,
// unmarshals into the target type, and validates required fields.
// T must be a struct type whose pointer implements jsonValidator.
func parseStructuredJSON[T any, PT interface {
	*T
	jsonValidator
}](output string) (*T, error) {
	jsonStr, err := extractJSON(output)
	if err != nil {
		return nil, fmt.Errorf("extract JSON: %w", err)
	}

	var payload T
	if err := json.Unmarshal([]byte(jsonStr), &payload); err != nil {
		return nil, fmt.Errorf("unmarshal JSON: %w", err)
	}

	if err := PT(&payload).validate(); err != nil {
		return nil, fmt.Errorf("validate payload: %w", err)
	}

	return &payload, nil
}

// --- state persistence ---

const deepPlanStateDir = ".ralphex/deep-plan"

// deepPlanStatePath returns the file path for deep plan state based on repo root + description hash.
func deepPlanStatePath(description string) string {
	repoHash := repoRootHash()
	descHash := shortHash(description)
	return filepath.Join(deepPlanStateDir, fmt.Sprintf("%s-%s.json", repoHash, descHash))
}

// repoRootHash returns a short hash of the git repo root directory.
func repoRootHash() string {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		return "unknown"
	}
	return shortHash(strings.TrimSpace(string(out)))
}

// shortHash returns the first 8 characters of SHA-256 hex digest.
func shortHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", h[:4])
}

// newDeepPlanProgress creates initial deep plan progress state.
func newDeepPlanProgress(description, toolVersion string) *deepPlanProgress {
	return &deepPlanProgress{
		Description:    description,
		CreatedAt:      time.Now().UTC().Format(time.RFC3339),
		ToolVersion:    toolVersion,
		Phase:          "exploration",
		States:         make(map[string]string),
		AgreedContent:  make(map[string]string),
		Decisions:      make(map[string]string),
		Iterations:     make(map[string]int),
	}
}

// saveDeepPlanState persists deep plan progress to disk.
func saveDeepPlanState(progress *deepPlanProgress) error {
	path := deepPlanStatePath(progress.Description)

	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}

	data, err := json.MarshalIndent(progress, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}

	return os.WriteFile(path, data, 0o640)
}

// loadDeepPlanState loads deep plan progress from disk.
// returns nil, nil if no state file exists.
func loadDeepPlanState(description string) (*deepPlanProgress, error) {
	path := deepPlanStatePath(description)

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read state file: %w", err)
	}

	var progress deepPlanProgress
	if err := json.Unmarshal(data, &progress); err != nil {
		return nil, fmt.Errorf("unmarshal state: %w", err)
	}

	return &progress, nil
}

// cleanupDeepPlanState removes the deep plan state file.
func cleanupDeepPlanState(description string) error {
	path := deepPlanStatePath(description)
	err := os.Remove(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove state file: %w", err)
	}
	return nil
}

// --- codex availability check ---

// isCodexAvailable checks if the codex CLI binary is available in PATH.
// this is the single shared helper used by both CLI validation and processor logic.
func isCodexAvailable(codexCommand string) bool {
	cmd := codexCommand
	if cmd == "" {
		cmd = "codex"
	}
	_, err := exec.LookPath(cmd)
	return err == nil
}

// --- section helpers ---

// enabledSections returns only sections that are enabled from the full list.
func enabledSections(sections []deepPlanSection) []deepPlanSection {
	var result []deepPlanSection
	for _, s := range sections {
		if s.Enabled {
			result = append(result, s)
		}
	}
	return result
}

// validateSectionInvariants checks that all required sections are present and enabled.
func validateSectionInvariants(sections []deepPlanSection) error {
	enabled := make(map[string]bool)
	for _, s := range sections {
		if s.Enabled {
			enabled[s.Name] = true
		}
	}
	for name := range requiredSections {
		if !enabled[name] {
			return fmt.Errorf("required section %q must be enabled", name)
		}
	}
	return nil
}

// defaultSections returns a copy of allSections with all sections enabled by default.
func defaultSections() []deepPlanSection {
	result := make([]deepPlanSection, len(allSections))
	copy(result, allSections)
	for i := range result {
		result[i].Enabled = true
	}
	return result
}
