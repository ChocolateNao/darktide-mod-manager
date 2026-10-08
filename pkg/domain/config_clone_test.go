package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestConfigCloneIsDeep(t *testing.T) {
	in := DefaultConfig()
	in.Sources = map[string]SourceConfig{
		"vortex": {Enabled: true, APIKey: "secret", Options: map[string]string{"page": "2"}},
		"local":  {Enabled: true, Paths: []string{"/a", "/b"}},
	}
	in.UI.Theme = "dark"

	out := in.Clone()
	assert.NotNil(t, out, "clone of a non-nil config must not be nil")

	assert.Equal(t, "dark", out.UI.Theme, "scalar field lost")

	vortexOut := out.Sources["vortex"]
	vortexOut.APIKey = "rotated"
	vortexOut.Options["page"] = "99"
	out.Sources["vortex"] = vortexOut

	localOut := out.Sources["local"]
	localOut.Paths[0] = "/mutated"
	out.Sources["local"] = localOut

	out.UI.Theme = "light"

	assert.Equal(
		t,
		"secret",
		in.Sources["vortex"].APIKey,
		"APIKey shared between original and clone",
	)

	assert.Equal(t, "2", in.Sources["vortex"].Options["page"], "source Options map shared")

	assert.Equal(t, "/a", in.Sources["local"].Paths[0], "source Paths slice shared")

	assert.Equal(t, "dark", in.UI.Theme, "nested scalar shared")

	out.SetSource("added", SourceConfig{Enabled: true})

	_, ok := in.SourceConfigFor("added")
	assert.False(t, ok, "adding a source to the clone leaked into the original")
}

func TestConfigCloneNilReceiver(t *testing.T) {
	var c *Config
	assert.Nil(t, c.Clone(), "nil config must clone to nil")
}

func TestConfigCloneAllocatesSourceMap(t *testing.T) {
	empty := Config{}

	out := empty.Clone()
	assert.NotNil(t, out.Sources, "clone should always have a usable Sources map")

	out.SetSource("x", SourceConfig{Enabled: true})

	sc, ok := out.SourceConfigFor("x")
	assert.True(t, ok && sc.Enabled, "SetSource on the clone failed: %+v %v", sc, ok)

	assert.Len(t, out.Sources, 1, "clone of an empty config has sources, want 1 after SetSource")
}

func TestConfigClonePreservesNilSourceFields(t *testing.T) {
	in := Config{Sources: map[string]SourceConfig{"local": {Enabled: true}}}
	out := in.Clone()

	sc := out.Sources["local"]
	assert.Nil(t, sc.Paths, "nil Paths should stay nil")

	assert.Nil(t, sc.Options, "nil Options should stay nil")

	_, ok := in.SourceConfigFor("local")
	assert.True(t, ok, "clone dropped a source entry")
}

func TestConfigCloneCopyIsIndependentOfLaterWrites(t *testing.T) {
	in := DefaultConfig()
	in.SetSource("vortex", SourceConfig{Enabled: false})
	snapshot := in.Clone()

	in.Sources["vortex"] = SourceConfig{
		Enabled: true,
		Paths:   []string{"/late"},
		Options: map[string]string{"k": "v"},
	}

	got := snapshot.Sources["vortex"]
	assert.False(t, got.Enabled, "snapshot observed a later Enabled write")

	assert.Len(t, got.Paths, 0, "snapshot observed a later Paths write")

	assert.Len(t, got.Options, 0, "snapshot observed a later Options write")
}
