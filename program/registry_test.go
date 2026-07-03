package program

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLookupUnknownReturnsNoOp(t *testing.T) {
	a := Lookup("/some/unknown/program")
	assert.IsType(t, NoOpAdapter{}, a, "unknown program should fall back to NoOpAdapter")
}

func TestLookupFirstMatchWins(t *testing.T) {
	// Save and restore registry.
	saved := registry
	t.Cleanup(func() { registry = saved })
	registry = nil

	Register(nameAdapter{"first", []string{"foo"}})
	Register(nameAdapter{"second", []string{"foo", "bar"}})

	got := Lookup("foo")
	assert.Equal(t, "first", got.Name(), "first matching adapter should win")
}

func TestNoOpAdapterDetectsNothing(t *testing.T) {
	a := NoOpAdapter{}
	status, prompt := a.Detect("anything at all")
	assert.Equal(t, StatusUnknown, status)
	assert.Nil(t, prompt)
}

// nameAdapter is a minimal Adapter keyed by exact program name matches, used
// only for registry tests.
type nameAdapter struct {
	n   string
	sel []string
}

func (a nameAdapter) Name() string                  { return a.n }
func (a nameAdapter) Matches(program string) bool {
	for _, s := range a.sel {
		if s == program {
			return true
		}
	}
	return false
}
func (a nameAdapter) Detect(string) (Status, *Prompt) { return StatusUnknown, nil }
