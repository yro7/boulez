package program

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// recordingResponder records every keystroke action invoked on it, so tests
// can assert that a Prompt.Resolve actually drives the responder the way the
// adapter claims (e.g. trust prompt -> TapEnter, not TapDAndEnter).
type recordingResponder struct {
	taps   int
	dTaps  int
	sent   []string
	errOn  int // if >0, the Nth call returns errFrom
	err    error
	called int
}

func (r *recordingResponder) TapEnter() error {
	r.called++
	r.taps++
	if r.errOn == r.called && r.err != nil {
		return r.err
	}
	return nil
}

func (r *recordingResponder) TapDAndEnter() error {
	r.called++
	r.dTaps++
	if r.errOn == r.called && r.err != nil {
		return r.err
	}
	return nil
}

func (r *recordingResponder) SendKeys(keys string) error {
	r.called++
	r.sent = append(r.sent, keys)
	if r.errOn == r.called && r.err != nil {
		return r.err
	}
	return nil
}

// --- ClaudeAdapter edge cases ---------------------------------------------

func TestClaudeAdapter_MatchesEdgeCases(t *testing.T) {
	a := ClaudeAdapter{}
	cases := []struct {
		prog string
		want bool
	}{
		{"claude", true},
		{"/usr/local/bin/claude", true},
		{"./claude", true},
		{"~/bin/claude", true},
		// "claude" must be a suffix; by HasSuffix semantics these match.
		{"superclaude", true},
		{"myclaude", true},
		// These do not end with "claude".
		{"claudecode", false},
		{"Claude", false}, // case sensitive
		{"CLAUDE", false},
		{"claude-code", false},
		{"", false},
		{"aider", false},
		{"gemini", false},
		{"pi", false},
	}
	for _, c := range cases {
		t.Run(c.prog, func(t *testing.T) {
			assert.Equal(t, c.want, a.Matches(c.prog))
		})
	}
}

func TestClaudeAdapter_DetectPriority(t *testing.T) {
	// When both a trust prompt and a ready prompt are present, trust wins
	// (it is checked first and is the more actionable signal).
	content := "Do you trust the files in this folder?\nNo, and tell Claude what to do differently"
	s, p := ClaudeAdapter{}.Detect(content)
	assert.Equal(t, StatusPermission, s)
	if assert.NotNil(t, p) {
		assert.Equal(t, PromptTrust, p.Kind)
	}
}

func TestClaudeAdapter_DetectMCPBeforeReady(t *testing.T) {
	content := "Detected new MCP server from config\nNo, and tell Claude what to do differently"
	s, p := ClaudeAdapter{}.Detect(content)
	assert.Equal(t, StatusPermission, s)
	if assert.NotNil(t, p) {
		assert.Equal(t, PromptTrust, p.Kind)
	}
}

func TestClaudeAdapter_DetectEmpty(t *testing.T) {
	s, p := ClaudeAdapter{}.Detect("")
	// Empty content has no ready/trust marker -> default Working.
	assert.Equal(t, StatusWorking, s)
	assert.Nil(t, p)
}

func TestClaudeAdapter_DetectSubstringsOnly(t *testing.T) {
	// Substrings embedded in larger words should still trigger detection
	// (Contains is intentionally substring-based, mirroring pre-refactor).
	s, p := ClaudeAdapter{}.Detect("blah Do you trust the files in this folder? blah")
	assert.Equal(t, StatusPermission, s)
	if assert.NotNil(t, p) {
		assert.Equal(t, PromptTrust, p.Kind)
	}
}

func TestClaudeAdapter_DetectMultilineFooter(t *testing.T) {
	// Real pane content is multiline; the ready marker is on its own line.
	content := strings.Join([]string{
		"some agent output",
		"? Yes  No, and tell Claude what to do differently",
		">",
	}, "\n")
	s, p := ClaudeAdapter{}.Detect(content)
	assert.Equal(t, StatusReady, s)
	if assert.NotNil(t, p) {
		assert.Equal(t, PromptReady, p.Kind)
	}
}

func TestClaudeAdapter_ResolveTrustInvokesTapEnter(t *testing.T) {
	// The trust prompt must be auto-resolved by tapping Enter (not 'D').
	_, p := ClaudeAdapter{}.Detect("Do you trust the files in this folder?")
	requirePrompt(t, p, PromptTrust)
	r := &recordingResponder{}
	assert.NoError(t, p.Resolve(r))
	assert.Equal(t, 1, r.taps, "trust resolve should TapEnter exactly once")
	assert.Equal(t, 0, r.dTaps, "trust resolve must not TapDAndEnter")
	assert.Empty(t, r.sent)
}

func TestClaudeAdapter_ResolveMCPInvokesTapEnter(t *testing.T) {
	_, p := ClaudeAdapter{}.Detect("new MCP server detected")
	requirePrompt(t, p, PromptTrust)
	r := &recordingResponder{}
	assert.NoError(t, p.Resolve(r))
	assert.Equal(t, 1, r.taps)
}

func TestClaudeAdapter_ReadyPromptHasNoResolve(t *testing.T) {
	// The ready prompt is informational only; there is nothing to auto-dismiss.
	s, p := ClaudeAdapter{}.Detect("No, and tell Claude what to do differently")
	assert.Equal(t, StatusReady, s)
	if assert.NotNil(t, p) {
		assert.Nil(t, p.Resolve, "ready prompt must not auto-resolve")
	}
}

func TestClaudeAdapter_ResolvePropagatesResponderError(t *testing.T) {
	// If the responder errors, Resolve must surface that error rather than
	// swallow it; the auto-yes daemon relies on this to back off.
	_, p := ClaudeAdapter{}.Detect("Do you trust the files in this folder?")
	requirePrompt(t, p, PromptTrust)
	r := &recordingResponder{errOn: 1, err: assertErr("boom")}
	err := p.Resolve(r)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "boom")
}

// --- AiderAdapter edge cases ----------------------------------------------

func TestAiderAdapter_MatchesPrefix(t *testing.T) {
	a := AiderAdapter{}
	// HasPrefix semantics, ported verbatim: "aideranything" matches.
	assert.True(t, a.Matches("aider"))
	assert.True(t, a.Matches("aider --model ollama_chat/gemma3:1b"))
	assert.True(t, a.Matches("aiderfoo"))              // prefix match, by design
	assert.False(t, a.Matches("Aider"))                // case sensitive
	assert.False(t, a.Matches("/usr/local/bin/aider")) // path not stripped (by design)
	assert.False(t, a.Matches(""))
	assert.False(t, a.Matches("claude"))
}

func TestAiderAdapter_DetectEmpty(t *testing.T) {
	s, p := AiderAdapter{}.Detect("")
	assert.Equal(t, StatusWorking, s)
	assert.Nil(t, p)
}

func TestAiderAdapter_DetectNoResolve(t *testing.T) {
	s, p := AiderAdapter{}.Detect("Edit files? (Y)es/(N)o/(D)on't ask again")
	assert.Equal(t, StatusReady, s)
	if assert.NotNil(t, p) {
		assert.Nil(t, p.Resolve, "aider ready prompt has no auto-resolve")
	}
}

// --- GeminiAdapter edge cases ---------------------------------------------

func TestGeminiAdapter_MatchesPrefix(t *testing.T) {
	a := GeminiAdapter{}
	assert.True(t, a.Matches("gemini"))
	assert.True(t, a.Matches("gemini --model x"))
	assert.True(t, a.Matches("geminifoo")) // prefix match, by design
	assert.False(t, a.Matches("Gemini"))
	assert.False(t, a.Matches(""))
	assert.False(t, a.Matches("claude"))
}

func TestGeminiAdapter_DetectEmpty(t *testing.T) {
	s, p := GeminiAdapter{}.Detect("")
	assert.Equal(t, StatusWorking, s)
	assert.Nil(t, p)
}

func TestGeminiAdapter_DetectNoResolve(t *testing.T) {
	s, p := GeminiAdapter{}.Detect("Run command? Yes, allow once")
	assert.Equal(t, StatusReady, s)
	if assert.NotNil(t, p) {
		assert.Nil(t, p.Resolve)
	}
}

// --- PiAdapter edge cases -------------------------------------------------

func TestPiAdapter_MatchesPathological(t *testing.T) {
	a := PiAdapter{}
	cases := []struct {
		prog string
		want bool
	}{
		{"pi", true},
		{"/opt/homebrew/bin/pi", true},
		{"/usr/local/bin/pi", true},
		{"./pi", true},
		{"~/bin/pi", true},
		// Trailing slash -> base becomes "" -> not "pi".
		{"/usr/bin/pi/", false},
		// Whitespace is trimmed, but "  pi  " base is not "pi" after path strip
		// because there is no slash; TrimSpace handles it.
		{"  pi  ", true},
		// "exec pi" form is NOT supported (only leading whitespace trimmed).
		{"exec pi", false},
		// Look-alikes must not match.
		{"ping", false},
		{"pixi", false},
		{"piano", false},
		{"", false},
		{"claude", false},
		{"aider", false},
		{"gemini", false},
		// Bare "pi" suffix via path: "/x/y/pi" matches, "/x/y/zip" does not.
		{"/x/y/zip", false},
	}
	for _, c := range cases {
		t.Run(c.prog, func(t *testing.T) {
			assert.Equal(t, c.want, a.Matches(c.prog))
		})
	}
}

func TestPiAdapter_DetectEmpty(t *testing.T) {
	s, p := PiAdapter{}.Detect("")
	assert.Equal(t, StatusUnknown, s, "empty content is not a recognisable state")
	assert.Nil(t, p)
}

func TestPiAdapter_DetectInvalidJSONIsUnknown(t *testing.T) {
	// Non-JSON content (e.g. a stale pane scrape) is not a journal line.
	s, p := PiAdapter{}.Detect("some output\nmore output")
	assert.Equal(t, StatusUnknown, s)
	assert.Nil(t, p)
}

func TestPiAdapter_DetectNonMessageEntryIsUnknown(t *testing.T) {
	// Non-message entries (session header, model_change) appear between
	// turns. Status cannot be inferred from them alone.
	s, p := PiAdapter{}.Detect(`{"type":"session","id":"abc"}`)
	assert.Equal(t, StatusUnknown, s)
	assert.Nil(t, p)
}

func TestPiAdapter_DetectAssistantStopIsReady(t *testing.T) {
	s, p := PiAdapter{}.Detect(`{"type":"message","message":{"role":"assistant","stopReason":"stop"}}`)
	assert.Equal(t, StatusReady, s)
	if assert.NotNil(t, p) {
		assert.Equal(t, PromptReady, p.Kind)
		assert.Nil(t, p.Resolve, "pi ready prompt has no auto-resolve")
	}
}

func TestPiAdapter_DetectAssistantToolUseIsWorking(t *testing.T) {
	s, p := PiAdapter{}.Detect(`{"type":"message","message":{"role":"assistant","stopReason":"toolUse"}}`)
	assert.Equal(t, StatusWorking, s)
	assert.Nil(t, p)
}

func TestPiAdapter_DetectToolResultIsWorking(t *testing.T) {
	// A trailing toolResult means the LLM will resume shortly.
	s, p := PiAdapter{}.Detect(`{"type":"message","message":{"role":"toolResult"}}`)
	assert.Equal(t, StatusWorking, s)
	assert.Nil(t, p)
}

// --- Registry edge cases --------------------------------------------------

func TestLookupEmptyStringReturnsNoOp(t *testing.T) {
	a := Lookup("")
	assert.IsType(t, NoOpAdapter{}, a)
}

func TestLookupEmptyRegistryReturnsNoOp(t *testing.T) {
	saved := registry
	t.Cleanup(func() { registry = saved })
	registry = nil

	got := Lookup("claude")
	assert.IsType(t, NoOpAdapter{}, got, "empty registry must fall back to NoOp")
}

func TestLookupLastRegisteredMatches(t *testing.T) {
	saved := registry
	t.Cleanup(func() { registry = saved })
	registry = nil

	Register(nameAdapter{"only", []string{"unique-prog"}})
	got := Lookup("unique-prog")
	assert.Equal(t, "only", got.Name())
}

func TestLookupNonMatchingRegisteredReturnsNoOp(t *testing.T) {
	saved := registry
	t.Cleanup(func() { registry = saved })
	registry = nil

	Register(nameAdapter{"a", []string{"foo"}})
	// A registered but non-matching adapter must not shadow NoOp.
	got := Lookup("bar")
	assert.IsType(t, NoOpAdapter{}, got)
}

func TestNoOpAdapterNeverMatches(t *testing.T) {
	a := NoOpAdapter{}
	// Even with agent-like inputs, NoOp must never claim to match.
	for _, prog := range []string{"pi", "claude", "aider", "gemini", "", "anything"} {
		assert.False(t, a.Matches(prog), "NoOp must not match %q", prog)
	}
}

func TestNoOpAdapterNameIsNoop(t *testing.T) {
	assert.Equal(t, "noop", NoOpAdapter{}.Name())
}

func TestBuiltinAdaptersRegistered(t *testing.T) {
	// The four built-in adapters must be reachable via Lookup using their
	// canonical command forms. Guards against a missing Register line in
	// program.go's init().
	cases := []struct {
		prog string
		name string
	}{
		{"claude", "claude"},
		{"aider", "aider"},
		{"gemini", "gemini"},
		{"pi", "pi"},
		{"/usr/local/bin/claude", "claude"},
		{"/opt/homebrew/bin/pi", "pi"},
	}
	for _, c := range cases {
		t.Run(c.prog, func(t *testing.T) {
			assert.Equal(t, c.name, Lookup(c.prog).Name())
		})
	}
}

func TestBuiltinLookupOrderClaudeBeforePi(t *testing.T) {
	// "claude" must resolve to the claude adapter, not be shadowed by a later
	// registered adapter that might also (mis)match it.
	got := Lookup("claude")
	assert.Equal(t, "claude", got.Name())
}

// --- Status / PromptKind constants ----------------------------------------

func TestStatusOrdering(t *testing.T) {
	// The zero value is StatusUnknown; the others are distinct and ordered.
	assert.Equal(t, StatusUnknown, Status(0))
	assert.NotEqual(t, StatusWorking, StatusReady)
	assert.NotEqual(t, StatusReady, StatusPermission)
	assert.NotEqual(t, StatusPermission, StatusUnknown)
}

func TestPromptKindOrdering(t *testing.T) {
	assert.Equal(t, PromptNone, PromptKind(0))
	assert.NotEqual(t, PromptTrust, PromptPermission)
	assert.NotEqual(t, PromptPermission, PromptReady)
}

// --- helpers --------------------------------------------------------------

type assertErr string

func (e assertErr) Error() string { return string(e) }

func requirePrompt(t *testing.T, p *Prompt, kind PromptKind) {
	t.Helper()
	if !assert.NotNil(t, p, "expected a prompt of kind %v", kind) {
		t.FailNow()
	}
	assert.Equal(t, kind, p.Kind)
	if p.Resolve == nil {
		t.Fatalf("prompt kind %v has no Resolve function", kind)
	}
}
