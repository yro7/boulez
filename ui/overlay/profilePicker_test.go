package overlay

import (
	"testing"

	"github.com/yro7/boulez/config"

	"github.com/stretchr/testify/assert"
)

func TestProfilePickerDefaultsToFirstProfile(t *testing.T) {
	pp := NewProfilePicker([]config.Profile{
		{Name: "Claude", Program: "claude"},
		{Name: "Pi", Program: "pi"},
	})
	assert.Equal(t, "Claude", pp.GetSelectedProfile().Name)
}

func TestProfilePickerSetSelectedByName(t *testing.T) {
	pp := NewProfilePicker([]config.Profile{
		{Name: "Claude", Program: "claude"},
		{Name: "Pi", Program: "pi"},
		{Name: "Gemini", Program: "gemini"},
	})

	pp.SetSelectedByName("Pi")
	assert.Equal(t, "Pi", pp.GetSelectedProfile().Name)
	assert.Equal(t, "pi", pp.GetSelectedProfile().Program)

	pp.SetSelectedByName("Gemini")
	assert.Equal(t, "Gemini", pp.GetSelectedProfile().Name)
}

func TestProfilePickerSetSelectedByNameIgnoresUnknown(t *testing.T) {
	pp := NewProfilePicker([]config.Profile{
		{Name: "Claude", Program: "claude"},
		{Name: "Pi", Program: "pi"},
	})
	// Select Pi first, then try an unknown name: selection must stay on Pi.
	pp.SetSelectedByName("Pi")
	pp.SetSelectedByName("nope")
	assert.Equal(t, "Pi", pp.GetSelectedProfile().Name)
}

func TestProfilePickerHasMultiple(t *testing.T) {
	assert.False(t, (&ProfilePicker{profiles: []config.Profile{{Name: "only"}}}).HasMultiple())
	assert.True(t, NewProfilePicker([]config.Profile{
		{Name: "a"}, {Name: "b"},
	}).HasMultiple())
}
