package gdrive

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEscapeNameClosesTheQueryLiteral(t *testing.T) {
	// Drive's query language ends a name literal at a quote, which would make a
	// name holding one match everything after it in the same query.
	assert.Equal(t, "mod-a.zip", escapeName("mod-a.zip"))
	assert.Equal(t, `keep\'it`, escapeName("keep'it"))
}

func TestParseSizeReadsADecimal(t *testing.T) {
	assert.Equal(t, int64(1234), parseSize("1234"))
	assert.Equal(t, int64(0), parseSize(""))
	assert.Equal(t, int64(0), parseSize("12abc"))
}

func TestProviderRequiresAFolder(t *testing.T) {
	_, err := New(Config{ClientID: "id"})
	assert.Error(t, err)
}
