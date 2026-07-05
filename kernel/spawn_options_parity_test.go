package kernel_test

import (
	"reflect"
	"sort"
	"testing"

	"github.com/yro7/boulez/app"
	"github.com/yro7/boulez/kernel"
)

// TestSpawnOptionsParity guards the app.SpawnOptions / kernel.SpawnOptions
// pair against silent drift. The two structs are deliberately separate (the
// kernel layer must not import the TUI-bearing app package), but they must
// carry the same set of EXPORTED fields with the same names and types: the
// app→kernel spawn path copies them field-by-field (kernel/transport.go's
// spawnParams.toOptions), so a field renamed or retyped on one side without
// the other is a wire-contract bug that compiles cleanly.
//
// Unexported fields (e.g. app.SpawnOptions.tmuxSession, a test-only seam) are
// ignored: they never cross the app→kernel boundary.
//
// This test lives in an external test package (kernel_test) so it can import
// app without forming an import cycle (app imports kernel at runtime).
func TestSpawnOptionsParity(t *testing.T) {
	appFields := exportedFields(t, app.SpawnOptions{})
	kernelFields := exportedFields(t, kernel.SpawnOptions{})

	// Compare as sorted sets: declaration order is irrelevant — the app→kernel
	// copy in transport.go assigns by field name, not position. What matters is
	// that no field is present on one side and missing/retyped on the other.
	if !reflect.DeepEqual(sorted(appFields), sorted(kernelFields)) {
		t.Fatalf("app.SpawnOptions and kernel.SpawnOptions exported field sets diverge:\n"+
			"  app.SpawnOptions:    %v\n"+
			"  kernel.SpawnOptions: %v\n"+
			"Adding/renaming/retyping a field on one side without the other breaks\n"+
			"the field-by-field copy in kernel/transport.go (spawnParams.toOptions).",
			appFields, kernelFields)
	}
}

// exportedFields returns a deterministic description of a struct type's
// exported fields: each entry is "name type". Unexported fields are skipped.
// The slice is NOT order-stable across the two structs (declaration order may
// differ); callers compare via sorted().
func exportedFields(t *testing.T, v any) []string {
	t.Helper()
	ty := reflect.TypeOf(v)
	if ty.Kind() != reflect.Struct {
		t.Fatalf("expected struct, got %s", ty.Kind())
	}
	out := make([]string, 0, ty.NumField())
	for i := 0; i < ty.NumField(); i++ {
		f := ty.Field(i)
		if !f.IsExported() {
			continue
		}
		out = append(out, f.Name+" "+f.Type.String())
	}
	return out
}

func sorted(s []string) []string {
	cp := append([]string(nil), s...)
	sort.Strings(cp)
	return cp
}
