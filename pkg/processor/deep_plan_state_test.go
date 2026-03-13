package processor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- extractJSON tests ---

func TestExtractJSON_SingleObject(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "clean JSON",
			input:    `{"schema_version": 1, "section": "architecture", "content": "## Arch"}`,
			expected: `{"schema_version": 1, "section": "architecture", "content": "## Arch"}`,
		},
		{
			name:     "JSON surrounded by text",
			input:    `Here is my proposal:\n{"schema_version": 1, "section": "tasks", "content": "## Tasks"}\nDone.`,
			expected: `{"schema_version": 1, "section": "tasks", "content": "## Tasks"}`,
		},
		{
			name:     "nested braces",
			input:    `{"schema_version": 1, "data": {"nested": {"deep": true}}}`,
			expected: `{"schema_version": 1, "data": {"nested": {"deep": true}}}`,
		},
		{
			name:     "braces in strings",
			input:    `{"content": "use {brackets} in code like obj.{field}"}`,
			expected: `{"content": "use {brackets} in code like obj.{field}"}`,
		},
		{
			name:     "escaped quotes in strings",
			input:    `{"content": "he said \"hello\" and {left}"}`,
			expected: `{"content": "he said \"hello\" and {left}"}`,
		},
		{
			name: "multiline with noise",
			input: `[10:30:05] analyzing...
I've completed the analysis. Here's my proposal:

{"schema_version": 1, "section": "testing", "content": "## Testing\n\nUse Jest."}

That's my proposal above.`,
			expected: `{"schema_version": 1, "section": "testing", "content": "## Testing\n\nUse Jest."}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, err := extractJSON(tc.input)
			require.NoError(t, err)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestExtractJSON_NoObject(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"empty string", ""},
		{"plain text", "no json here at all"},
		{"only arrays", `[1, 2, 3]`},
		{"only closing brace", `}`},
		{"incomplete object", `{"key": "value`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := extractJSON(tc.input)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "no JSON object found")
		})
	}
}

func TestExtractJSON_MultipleObjects(t *testing.T) {
	input := `{"first": 1} some text {"second": 2}`
	_, err := extractJSON(input)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected exactly 1 JSON object, found 2")
}

func TestExtractJSON_DirtyInput(t *testing.T) {
	// LLM output with markdown code block wrapping
	input := "```json\n{\"schema_version\": 1, \"section\": \"arch\", \"content\": \"test\"}\n```"
	result, err := extractJSON(input)
	require.NoError(t, err)
	assert.Equal(t, `{"schema_version": 1, "section": "arch", "content": "test"}`, result)
}

// --- parseStructuredJSON tests ---

func TestParseStructuredJSON_Proposal(t *testing.T) {
	input := `Here's the proposal:
{"schema_version": 1, "section": "architecture", "content": "## Architecture\n\nUse microservices."}
Done.`

	result, err := parseStructuredJSON[deepPlanProposalPayload, *deepPlanProposalPayload](input)
	require.NoError(t, err)
	assert.Equal(t, 1, result.SchemaVersion)
	assert.Equal(t, "architecture", result.Section)
	assert.Contains(t, result.Content, "microservices")
}

func TestParseStructuredJSON_Critique(t *testing.T) {
	input := `{"schema_version": 1, "section": "tasks", "issues": [{"message": "Missing error handling", "severity": "major"}], "verdict": "needs_revision"}`

	result, err := parseStructuredJSON[deepPlanCritiquePayload, *deepPlanCritiquePayload](input)
	require.NoError(t, err)
	assert.Equal(t, "needs_revision", result.Verdict)
	assert.Len(t, result.Issues, 1)
	assert.Equal(t, "Missing error handling", result.Issues[0].Message)
}

func TestParseStructuredJSON_CritiqueAcceptable(t *testing.T) {
	input := `{"schema_version": 1, "section": "testing", "issues": [], "verdict": "acceptable"}`

	result, err := parseStructuredJSON[deepPlanCritiquePayload, *deepPlanCritiquePayload](input)
	require.NoError(t, err)
	assert.Equal(t, "acceptable", result.Verdict)
	assert.Empty(t, result.Issues)
}

func TestParseStructuredJSON_Revision(t *testing.T) {
	input := `{"schema_version": 1, "section": "tasks", "content": "## Revised Tasks", "addressed": ["error handling"], "disagreed": ["naming"]}`

	result, err := parseStructuredJSON[deepPlanRevisionPayload, *deepPlanRevisionPayload](input)
	require.NoError(t, err)
	assert.Equal(t, "## Revised Tasks", result.Content)
	assert.Equal(t, []string{"error handling"}, result.Addressed)
	assert.Equal(t, []string{"naming"}, result.Disagreed)
}

func TestParseStructuredJSON_Lint(t *testing.T) {
	input := `{"schema_version": 1, "issues": [{"message": "tasks don't match architecture", "affected_sections": ["tasks", "architecture"]}], "verdict": "needs_revision"}`

	result, err := parseStructuredJSON[deepPlanLintPayload, *deepPlanLintPayload](input)
	require.NoError(t, err)
	assert.Equal(t, "needs_revision", result.Verdict)
	require.Len(t, result.Issues, 1)
	assert.Equal(t, []string{"tasks", "architecture"}, result.Issues[0].AffectedSections)
}

func TestParseStructuredJSON_LintPass(t *testing.T) {
	input := `{"schema_version": 1, "issues": [], "verdict": "pass"}`

	result, err := parseStructuredJSON[deepPlanLintPayload, *deepPlanLintPayload](input)
	require.NoError(t, err)
	assert.Equal(t, "pass", result.Verdict)
}

func TestParseStructuredJSON_ValidationError(t *testing.T) {
	// wrong schema version
	input := `{"schema_version": 99, "section": "arch", "content": "test"}`
	_, err := parseStructuredJSON[deepPlanProposalPayload, *deepPlanProposalPayload](input)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported schema_version")

	// missing required field
	input = `{"schema_version": 1, "section": "", "content": "test"}`
	_, err = parseStructuredJSON[deepPlanProposalPayload, *deepPlanProposalPayload](input)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing section field")

	// invalid verdict
	input = `{"schema_version": 1, "section": "x", "issues": [], "verdict": "maybe"}`
	_, err = parseStructuredJSON[deepPlanCritiquePayload, *deepPlanCritiquePayload](input)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid verdict")
}

func TestParseStructuredJSON_NoJSON(t *testing.T) {
	_, err := parseStructuredJSON[deepPlanProposalPayload, *deepPlanProposalPayload]("no json here")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "extract JSON")
}

func TestParseStructuredJSON_LintIssueValidation(t *testing.T) {
	// issue missing message
	input := `{"schema_version": 1, "issues": [{"message": "", "affected_sections": ["x"]}], "verdict": "needs_revision"}`
	_, err := parseStructuredJSON[deepPlanLintPayload, *deepPlanLintPayload](input)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing message")

	// issue missing affected_sections
	input = `{"schema_version": 1, "issues": [{"message": "problem"}], "verdict": "needs_revision"}`
	_, err = parseStructuredJSON[deepPlanLintPayload, *deepPlanLintPayload](input)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing affected_sections")
}

// --- state persistence tests ---

func TestSaveLoadCleanupState(t *testing.T) {
	// use temp dir so we don't pollute the real project
	origDir, err := os.Getwd()
	require.NoError(t, err)
	tmpDir := t.TempDir()
	require.NoError(t, os.Chdir(tmpDir))
	defer func() { _ = os.Chdir(origDir) }()

	progress := newDeepPlanProgress("test description", "0.21.0")
	progress.Phase = "section_loop"
	progress.Sections = defaultSections()
	progress.States["architecture"] = deepPlanStateAgreed
	progress.AgreedContent["architecture"] = "## Architecture\n\nContent here."
	progress.Decisions["architecture/caching"] = "Option A chosen by user"
	progress.CurrentSection = 1

	// save
	err = saveDeepPlanState(progress)
	require.NoError(t, err)

	// verify file exists
	path := deepPlanStatePath("test description")
	_, err = os.Stat(path)
	require.NoError(t, err)

	// load
	loaded, err := loadDeepPlanState("test description")
	require.NoError(t, err)
	require.NotNil(t, loaded)

	assert.Equal(t, "test description", loaded.Description)
	assert.Equal(t, "0.21.0", loaded.ToolVersion)
	assert.Equal(t, "section_loop", loaded.Phase)
	assert.Equal(t, 1, loaded.CurrentSection)
	assert.Equal(t, deepPlanStateAgreed, loaded.States["architecture"])
	assert.Contains(t, loaded.AgreedContent["architecture"], "Architecture")
	assert.Equal(t, "Option A chosen by user", loaded.Decisions["architecture/caching"])
	assert.Len(t, loaded.Sections, 6)

	// cleanup
	err = cleanupDeepPlanState("test description")
	require.NoError(t, err)

	// verify file removed
	_, err = os.Stat(path)
	assert.True(t, os.IsNotExist(err))
}

func TestLoadState_NoFile(t *testing.T) {
	origDir, err := os.Getwd()
	require.NoError(t, err)
	tmpDir := t.TempDir()
	require.NoError(t, os.Chdir(tmpDir))
	defer func() { _ = os.Chdir(origDir) }()

	loaded, err := loadDeepPlanState("nonexistent")
	require.NoError(t, err)
	assert.Nil(t, loaded)
}

func TestCleanupState_NoFile(t *testing.T) {
	origDir, err := os.Getwd()
	require.NoError(t, err)
	tmpDir := t.TempDir()
	require.NoError(t, os.Chdir(tmpDir))
	defer func() { _ = os.Chdir(origDir) }()

	// should not error when file doesn't exist
	err = cleanupDeepPlanState("nonexistent")
	require.NoError(t, err)
}

// --- section helper tests ---

func TestDefaultSections(t *testing.T) {
	sections := defaultSections()
	assert.Len(t, sections, 6)

	// all enabled
	for _, s := range sections {
		assert.True(t, s.Enabled, "section %s should be enabled", s.Name)
	}

	// first 3 are required
	for _, s := range sections[:3] {
		assert.True(t, s.Required, "section %s should be required", s.Name)
	}

	// last 3 are optional
	for _, s := range sections[3:] {
		assert.False(t, s.Required, "section %s should be optional", s.Name)
	}
}

func TestValidateSectionInvariants_AllRequired(t *testing.T) {
	sections := defaultSections()
	err := validateSectionInvariants(sections)
	assert.NoError(t, err)
}

func TestValidateSectionInvariants_MissingRequired(t *testing.T) {
	sections := defaultSections()
	// disable "testing"
	for i := range sections {
		if sections[i].Name == "testing" {
			sections[i].Enabled = false
		}
	}

	err := validateSectionInvariants(sections)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "testing")
	assert.Contains(t, err.Error(), "must be enabled")
}

func TestValidateSectionInvariants_OptionalDisabled(t *testing.T) {
	sections := defaultSections()
	// disable optional sections
	for i := range sections {
		if !sections[i].Required {
			sections[i].Enabled = false
		}
	}

	err := validateSectionInvariants(sections)
	assert.NoError(t, err)
}

func TestEnabledSections(t *testing.T) {
	sections := defaultSections()
	sections[4].Enabled = false // disable api_design
	sections[5].Enabled = false // disable migration

	enabled := enabledSections(sections)
	assert.Len(t, enabled, 4)

	names := make([]string, len(enabled))
	for i, s := range enabled {
		names[i] = s.Name
	}
	assert.Equal(t, []string{"architecture", "tasks", "testing", "data_models"}, names)
}

// --- isCodexAvailable tests ---

func TestIsCodexAvailable_RealBinary(t *testing.T) {
	// "ls" should be available on all systems
	assert.True(t, isCodexAvailable("ls"))
}

func TestIsCodexAvailable_NonexistentBinary(t *testing.T) {
	assert.False(t, isCodexAvailable("nonexistent-binary-that-does-not-exist-12345"))
}

func TestIsCodexAvailable_DefaultCommand(t *testing.T) {
	// empty string → defaults to "codex", likely not installed in test env
	// just verify it doesn't panic
	_ = isCodexAvailable("")
}

// --- shortHash tests ---

func TestShortHash(t *testing.T) {
	h1 := shortHash("hello")
	h2 := shortHash("world")
	assert.Len(t, h1, 8)
	assert.Len(t, h2, 8)
	assert.NotEqual(t, h1, h2)

	// deterministic
	assert.Equal(t, h1, shortHash("hello"))
}

// --- deepPlanStatePath tests ---

func TestDeepPlanStatePath(t *testing.T) {
	path := deepPlanStatePath("my plan description")
	assert.True(t, filepath.IsLocal(path))
	assert.Contains(t, path, deepPlanStateDir)
	assert.Contains(t, path, ".json")

	// different descriptions → different paths
	path2 := deepPlanStatePath("another description")
	assert.NotEqual(t, path, path2)
}

// --- deep plan conflict signal tests ---

func TestParseDeepPlanConflictPayload_Valid(t *testing.T) {
	output := `some output
<<<RALPHEX:DEEP_PLAN_CONFLICT>>>
{"section": "architecture", "topic": "caching strategy", "proposer_argument": "Use Redis", "reviewer_argument": "Use in-memory", "options": ["Redis", "In-memory", "Hybrid"]}
<<<RALPHEX:END>>>
more output`

	result, err := ParseDeepPlanConflictPayload(output)
	require.NoError(t, err)
	assert.Equal(t, "architecture", result.Section)
	assert.Equal(t, "caching strategy", result.Topic)
	assert.Equal(t, "Use Redis", result.ProposerArgument)
	assert.Equal(t, "Use in-memory", result.ReviewerArgument)
	assert.Equal(t, []string{"Redis", "In-memory", "Hybrid"}, result.Options)
}

func TestParseDeepPlanConflictPayload_NoSignal(t *testing.T) {
	_, err := ParseDeepPlanConflictPayload("no signal here")
	require.ErrorIs(t, err, ErrNoDeepPlanConflictSignal)
}

func TestParseDeepPlanConflictPayload_Malformed(t *testing.T) {
	tests := []struct {
		name        string
		output      string
		errContains string
	}{
		{
			name: "missing end marker",
			output: `<<<RALPHEX:DEEP_PLAN_CONFLICT>>>
{"section": "x", "topic": "y", "options": ["A"]}`,
			errContains: "missing END marker",
		},
		{
			name: "empty payload",
			output: `<<<RALPHEX:DEEP_PLAN_CONFLICT>>>
<<<RALPHEX:END>>>`,
			errContains: "empty JSON payload",
		},
		{
			name: "missing section",
			output: `<<<RALPHEX:DEEP_PLAN_CONFLICT>>>
{"section": "", "topic": "x", "options": ["A"]}
<<<RALPHEX:END>>>`,
			errContains: "missing section",
		},
		{
			name: "missing topic",
			output: `<<<RALPHEX:DEEP_PLAN_CONFLICT>>>
{"section": "x", "topic": "", "options": ["A"]}
<<<RALPHEX:END>>>`,
			errContains: "missing topic",
		},
		{
			name: "missing options",
			output: `<<<RALPHEX:DEEP_PLAN_CONFLICT>>>
{"section": "x", "topic": "y", "options": []}
<<<RALPHEX:END>>>`,
			errContains: "missing or empty options",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, err := ParseDeepPlanConflictPayload(tc.output)
			assert.Nil(t, result)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.errContains)
		})
	}
}

// --- newDeepPlanProgress tests ---

func TestNewDeepPlanProgress(t *testing.T) {
	p := newDeepPlanProgress("add user auth", "0.21.0")
	assert.Equal(t, "add user auth", p.Description)
	assert.Equal(t, "0.21.0", p.ToolVersion)
	assert.Equal(t, "exploration", p.Phase)
	assert.NotEmpty(t, p.CreatedAt)
	assert.NotNil(t, p.States)
	assert.NotNil(t, p.AgreedContent)
	assert.NotNil(t, p.Decisions)
	assert.NotNil(t, p.Iterations)
	assert.Empty(t, p.Sections)
}

// --- state file path uniqueness ---

func TestDeepPlanStatePath_SameDescription(t *testing.T) {
	// same description → same path
	path1 := deepPlanStatePath("add caching")
	path2 := deepPlanStatePath("add caching")
	assert.Equal(t, path1, path2)
}
