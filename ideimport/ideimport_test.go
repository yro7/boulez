package ideimport

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKnownIDEs_CountAndCanonical(t *testing.T) {
	// Guards against accidental removal of an IDE or a duplicate canon.
	assert.Len(t, knownIDEs, 8)
	seen := make(map[string]bool, len(knownIDEs))
	for _, s := range knownIDEs {
		assert.False(t, seen[s.canon], "duplicate canon %q", s.canon)
		seen[s.canon] = true
		assert.NotEmpty(t, s.name)
		assert.NotEmpty(t, s.dirName)
		assert.NotEmpty(t, s.canon)
	}
}

func TestLookupIDE(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"vscode", true},
		{"cursor", true},
		{"windsurf", true},
		{"antigravity", true},
		{"vscodium", true},
		{"pearai", true},
		{"void", true},
		{"trae", true},
		{"", false},
		{"foo", false},
		{"VS Code", false}, // display name is not a valid canon
		{"Code", false},    // dirName is not a valid canon
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec, ok := lookupIDE(tc.name)
			assert.Equal(t, tc.want, ok)
			if ok {
				assert.Equal(t, tc.name, spec.canon)
				assert.NotEmpty(t, spec.name)
				assert.NotEmpty(t, spec.dirName)
			} else {
				assert.Equal(t, ideSpec{}, spec)
			}
		})
	}
}

func TestValidIDENames(t *testing.T) {
	got := validIDENames()
	// All canonical names appear in the error hint.
	for _, s := range knownIDEs {
		assert.Contains(t, got, s.canon)
	}
}

func TestConfigRoot(t *testing.T) {
	cases := []struct {
		goos, home, want string
	}{
		{"darwin", "/Users/u", "/Users/u/Library/Application Support"},
		{"linux", "/home/u", "/home/u/.config"},
	}
	for _, tc := range cases {
		t.Run(tc.goos, func(t *testing.T) {
			got, err := configRoot(tc.goos, tc.home)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
	// Windows falls back to a homeDir-relative path when APPDATA is unset
	// (it is unset on the macOS test host).
	t.Run("windows_fallback", func(t *testing.T) {
		t.Setenv("APPDATA", "")
		got, err := configRoot("windows", "/h")
		require.NoError(t, err)
		assert.Equal(t, "/h/AppData/Roaming", got)
	})
	t.Run("windows_appdata", func(t *testing.T) {
		t.Setenv("APPDATA", "/custom")
		got, err := configRoot("windows", "/h")
		require.NoError(t, err)
		assert.Equal(t, "/custom", got)
	})
	t.Run("unsupported", func(t *testing.T) {
		_, err := configRoot("plan9", "/h")
		require.Error(t, err)
	})
}

func TestStoragePath(t *testing.T) {
	spec := ideSpec{name: "VS Code", dirName: "Code", canon: "vscode"}

	t.Run("darwin", func(t *testing.T) {
		got, err := storagePath("darwin", "/Users/u", spec)
		require.NoError(t, err)
		assert.Equal(t,
			"/Users/u/Library/Application Support/Code/User/globalStorage/storage.json", got)
	})

	t.Run("linux", func(t *testing.T) {
		got, err := storagePath("linux", "/home/u", spec)
		require.NoError(t, err)
		assert.Equal(t,
			"/home/u/.config/Code/User/globalStorage/storage.json", got)
	})

	t.Run("unsupported_os_propagates", func(t *testing.T) {
		_, err := storagePath("plan9", "/h", spec)
		require.Error(t, err)
	})
}
