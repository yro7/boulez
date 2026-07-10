package app

import (
	"github.com/charmbracelet/lipgloss"

	"github.com/yro7/boulez/log"
	"github.com/yro7/boulez/session"
	"github.com/yro7/boulez/ui/overlay"
)

// This file holds the pure-presentation View: composing the list, tabbed
// preview/diff window, menu, and error box, and layering whichever overlay
// corresponds to the active state on top. pinOrchestratorsFirst keeps the
// orchestrator (boulez's instance 0) at the head of the list on open.
// Extracted from app.go. Pure move: same receiver / signatures / behavior.

func (m *home) View() string {
	listWithPadding := lipgloss.NewStyle().PaddingTop(1).Render(m.list.String())
	previewWithPadding := lipgloss.NewStyle().PaddingTop(1).Render(m.tabbedWindow.String())
	listAndPreview := lipgloss.JoinHorizontal(lipgloss.Top, listWithPadding, previewWithPadding)

	mainView := lipgloss.JoinVertical(
		lipgloss.Center,
		listAndPreview,
		m.menu.String(),
		m.errBox.String(),
	)

	if m.state == statePrompt {
		if m.textInputOverlay == nil {
			log.ErrorLog.Printf("text input overlay is nil")
		}
		return overlay.PlaceOverlay(0, 0, m.textInputOverlay.Render(), mainView, true, true)
	} else if m.state == stateHelp {
		if m.textOverlay == nil {
			log.ErrorLog.Printf("text overlay is nil")
		}
		return overlay.PlaceOverlay(0, 0, m.textOverlay.Render(), mainView, true, true)
	} else if m.state == stateConfirm {
		if m.confirmationOverlay == nil {
			log.ErrorLog.Printf("confirmation overlay is nil")
		}
		return overlay.PlaceOverlay(0, 0, m.confirmationOverlay.Render(), mainView, true, true)
	} else if m.state == stateHostSelect {
		if m.hostSelector == nil {
			log.ErrorLog.Printf("host selector is nil")
			return mainView
		}
		return overlay.PlaceOverlay(0, 0, m.hostSelector.Render(), mainView, true, true)
	} else if m.state == stateRepoSelect {
		if m.repoSelector == nil {
			log.ErrorLog.Printf("repo selector is nil")
			return mainView
		}
		return overlay.PlaceOverlay(0, 0, m.repoSelector.Render(), mainView, true, true)
	} else if m.state == statePresetSelect {
		if m.presetSelector == nil {
			log.ErrorLog.Printf("preset selector is nil")
			return mainView
		}
		return overlay.PlaceOverlay(0, 0, m.presetSelector.Render(), mainView, true, true)
	} else if m.state == stateArchiveSelect {
		if m.archiveSelector == nil {
			log.ErrorLog.Printf("archive selector is nil")
			return mainView
		}
		return overlay.PlaceOverlay(0, 0, m.archiveSelector.Render(), mainView, true, true)
	}

	return mainView
}

// pinOrchestratorsFirst performs a stable partition of the loaded instances:
// all KindOrchestrator instances come first, then all workers, with the
// relative order within each group preserved. This guarantees the
// orchestrator (boulez's "instance 0") is at the head of the list on boulez open,
// so the default selection (index 0) lands on it and the user can interact
// with it immediately. A simple two-slice split+concat is stable by
// construction and avoids pulling in sort.Slice (whose stability is not
// guaranteed for the zero-struct comparator we'd otherwise need).
func pinOrchestratorsFirst(instances []*session.Instance) []*session.Instance {
	if len(instances) <= 1 {
		return instances
	}
	orchs := make([]*session.Instance, 0, len(instances))
	workers := make([]*session.Instance, 0, len(instances))
	for _, in := range instances {
		if in.Kind() == session.KindOrchestrator {
			orchs = append(orchs, in)
		} else {
			workers = append(workers, in)
		}
	}
	return append(orchs, workers...)
}
