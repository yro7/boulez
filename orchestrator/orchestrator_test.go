package orchestrator

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// fakeAPI is an in-memory API for testing Ensure without tmux, the kernel, or
// an LLM. It records every call so tests can assert the bootstrap behaviour.
type fakeAPI struct {
	orchs     []Instance
	fleet     []Instance
	spawned   []string // programs passed to SpawnOrchestrator
	spawnID   string
	spawnErr  error
	prompts   []promptCall
	promptErr error
}

type promptCall struct {
	id, prompt string
}

func (f *fakeAPI) ListInstances() []Instance { return f.fleet }

func (f *fakeAPI) ListOrchestrators() []Instance { return f.orchs }

func (f *fakeAPI) SpawnOrchestrator(program string) (string, error) {
	f.spawned = append(f.spawned, program)
	if f.spawnErr != nil {
		return "", f.spawnErr
	}
	id := f.spawnID
	if id == "" {
		id = "orch-new"
	}
	// Reflect the new orchestrator into the fleet views so a subsequent
	// ListInstances (the injection) sees it.
	f.orchs = append(f.orchs, Instance{ID: id, Kind: "orchestrator"})
	f.fleet = append(f.fleet, Instance{ID: id, Kind: "orchestrator"})
	return id, nil
}

func (f *fakeAPI) SendPrompt(id, prompt string) error {
	f.prompts = append(f.prompts, promptCall{id, prompt})
	return f.promptErr
}

// TestEnsure_FirstCreation_SpawnsAndInjectsOnce is the core contract: on a
// fresh fleet (no orchestrator), Ensure spawns one, writes the context file,
// and injects the fleet snapshot exactly once.
func TestEnsure_FirstCreation_SpawnsAndInjectsOnce(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	worker := Instance{ID: "w1", Kind: "worker", Status: "running", Title: "fix-bug"}
	api := &fakeAPI{
		fleet:   []Instance{worker},
		spawnID: "orch-1",
	}

	if _, err := Ensure(api, "pi"); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	if got := len(api.spawned); got != 1 {
		t.Fatalf("expected 1 spawn, got %d", got)
	}
	if api.spawned[0] != "pi" {
		t.Errorf("spawned program = %q, want %q", api.spawned[0], "pi")
	}
	if len(api.prompts) != 1 {
		t.Fatalf("expected exactly 1 injected prompt, got %d", len(api.prompts))
	}
	if api.prompts[0].id != "orch-1" {
		t.Errorf("prompt sent to %q, want orch-1", api.prompts[0].id)
	}
	// The injected prompt must carry the fleet snapshot.
	if !strings.Contains(api.prompts[0].prompt, "w1") {
		t.Errorf("injected prompt does not contain the fleet snapshot:\n%s", api.prompts[0].prompt)
	}
	if !strings.Contains(api.prompts[0].prompt, "ORCHESTRATOR.md") {
		t.Errorf("injected prompt does not point at ORCHESTRATOR.md")
	}

	// The context file must have been written to the control dir.
	dir, err := ControlDir("orch-1")
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dir + "/" + ContextFileName)
	if err != nil {
		t.Fatalf("context file not written: %v", err)
	}
	if !strings.Contains(string(got), "orch-1") {
		t.Errorf("context file does not mention the orchestrator ID")
	}
}

// TestEnsure_Restart_DoesNotReinject proves the idempotent path: when an
// orchestrator already exists, Ensure refreshes the context file but does
// NOT spawn or inject a prompt again.
func TestEnsure_Restart_DoesNotReinject(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	existing := Instance{ID: "orch-existing", Kind: "orchestrator", Status: "running"}
	api := &fakeAPI{
		orchs: []Instance{existing},
		fleet: []Instance{existing},
	}

	if _, err := Ensure(api, "pi"); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	if len(api.spawned) != 0 {
		t.Errorf("restart must not spawn, got %d spawns", len(api.spawned))
	}
	if len(api.prompts) != 0 {
		t.Errorf("restart must not inject a prompt, got %d prompts", len(api.prompts))
	}
	// But the context file IS refreshed (so docs stay current across upgrades).
	dir, err := ControlDir("orch-existing")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.ReadFile(dir + "/" + ContextFileName); err != nil {
		t.Errorf("restart must refresh the context file: %v", err)
	}
}

// TestEnsure_SpawnErrorPropagates proves a spawn failure aborts Ensure and
// does not inject.
func TestEnsure_SpawnErrorPropagates(t *testing.T) {
	api := &fakeAPI{spawnErr: errors.New("tmux down")}

	_, err := Ensure(api, "pi")
	if err == nil || !strings.Contains(err.Error(), "tmux down") {
		t.Fatalf("expected spawn error to propagate, got %v", err)
	}
	if len(api.prompts) != 0 {
		t.Errorf("must not inject on spawn failure, got %d prompts", len(api.prompts))
	}
}

// TestEnsure_PromptErrorPropagates proves a SendPrompt failure surfaces (the
// orchestrator is started + context written, but the injection failed).
func TestEnsure_PromptErrorPropagates(t *testing.T) {
	api := &fakeAPI{spawnID: "orch-1", promptErr: errors.New("pane gone")}

	_, err := Ensure(api, "pi")
	if err == nil || !strings.Contains(err.Error(), "pane gone") {
		t.Fatalf("expected prompt error to propagate, got %v", err)
	}
}

// TestRenderFleet_Empty proves the empty-fleet case renders a clear placeholder
// (the orchestrator must not see an empty string and think the tool failed).
func TestRenderFleet_Empty(t *testing.T) {
	got := RenderFleet(nil)
	if !strings.Contains(got, "no instances") {
		t.Errorf("empty fleet render should say so, got %q", got)
	}
}

// TestContextContent_DocumentsToolSurface proves the context file documents
// every syscall the agent can call. This is a lightweight DRY pin: if a syscall
// is added to the CLI, this test reminds you to document it here.
func TestContextContent_DocumentsToolSurface(t *testing.T) {
	doc := ContextContent("orch-1")
	want := []string{
		"list_instances",
		"spawn_worker",
		"get_instance",
		"send_prompt",
		"pause",
		"resume",
		"kill",
		"merge",
		"PROTECTED_BRANCH",
		"BRANCH_NOT_FOUND",
		"UNKNOWN_INSTANCE",
		"orch-1",
	}
	for _, w := range want {
		if !strings.Contains(doc, w) {
			t.Errorf("context file missing %q", w)
		}
	}
}
