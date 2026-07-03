package overlay

// PresetSelector is a thin configuration of ListSelector for choosing a named
// quick-session preset at instance creation (Ctrl+R). Unlike the repo/host
// selectors it does not accept free-text: a preset must already exist in
// ~/.cs2/presets.json (authored by the user or an agent). An empty preset list
// is handled by the caller, which shows an error pointing at the file.
//
// All list behaviour (cursor, typing to filter, submit, cancel) lives in the
// embedded ListSelector; this type only adds preset-specific accessors.
type PresetSelector struct {
	*ListSelector
}

// presetHints is the help line shown at the bottom of the preset selector.
const presetHints = "type to filter · ↑↓ move · enter select · esc cancel"

// NewPresetSelector creates a selector pre-populated with the given preset
// names. No free-text row: choosing a preset that does not exist is not a
// meaningful action.
func NewPresetSelector(names []string) *PresetSelector {
	items := make([]selectorItem, 0, len(names))
	for _, name := range names {
		items = append(items, selectorItem{label: name, value: name, deletable: false})
	}
	return &PresetSelector{
		ListSelector: NewListSelector("Quick session", items, false, "Filter: ", presetHints),
	}
}

// SelectedPreset returns the name of the preset chosen by the user, or "" if
// nothing was selected or the overlay was canceled.
func (p *PresetSelector) SelectedPreset() string { return p.SelectedValue() }
