package overlay

// HostSelector is a thin configuration of ListSelector for choosing an
// execution host (local or a known ssh alias) at instance creation. The
// "local" host is always offered as the first row and is non-deletable; the
// free-text input (last row) accepts an ssh alias not yet in the registry.
// The chosen alias is validated/reached by the caller (not here).
//
// All list behaviour (cursor, typing, submit, cancel, delete) lives in the
// embedded ListSelector; this type only adds host-specific accessors and the
// "local" row.
type HostSelector struct {
	*ListSelector
}

// localLabel is the label shown for the implicit local host row.
const localLabel = "local"

// localHints is the help line shown at the bottom of the host selector.
const localHints = "type to filter · ↑↓ move · ctrl+d delete · enter select · esc cancel"

// NewHostSelector creates a selector pre-populated with the given known ssh
// aliases (local is always prepended as the first, non-deletable row).
func NewHostSelector(hosts []string) *HostSelector {
	items := make([]selectorItem, 0, len(hosts)+1)
	items = append(items, selectorItem{
		label:     localLabel + "  (this machine)",
		value:     localLabel,
		deletable: false,
	})
	for _, alias := range hosts {
		items = append(items, selectorItem{label: alias, value: alias, deletable: true})
	}
	return &HostSelector{ListSelector: NewListSelector("Select host", items, true, "Alias: ", localHints)}
}

// SelectedAlias returns the alias chosen by the user. "local" for the local
// row; the alias for a known-host row; the typed text for the free-text row
// (which may be empty). Returns "" if nothing was selected or the overlay was
// canceled.
func (h *HostSelector) SelectedAlias() string { return h.SelectedValue() }

// IsFreeAlias reports whether the selection came from the free-text input (the
// caller uses this to decide whether to register the alias in the registry).
func (h *HostSelector) IsFreeAlias() bool { return h.IsFreeValue() }

// isLocalRow reports whether the cursor is on the "local" row.
func (h *HostSelector) isLocalRow() bool {
	items := h.filteredItems()
	return h.cursor < len(items) && items[h.cursor].value == localLabel
}

// isFreeAliasRow reports whether a submit would resolve to a free-text alias
// (i.e. no item is highlighted). Kept for compatibility with the fused model.
func (h *HostSelector) isFreeAliasRow() bool { return h.isFreeRow() }
