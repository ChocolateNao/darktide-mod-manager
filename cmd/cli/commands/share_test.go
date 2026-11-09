package commands

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseCSV(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{name: "empty"},
		// Separators and whitespace only yield an empty slice.
		{name: "whitespace only", in: "   ", want: []string{}},
		{name: "one", in: "better-ui", want: []string{"better-ui"}},
		{name: "three trimmed", in: " a , b , c ", want: []string{"a", "b", "c"}},
		// An empty segment is dropped: "a,,b" is a typo, and "mod \"\" not found"
		// points at a mod never asked for.
		{name: "empty segments dropped", in: "a,,b,", want: []string{"a", "b"}},
		{name: "no spaces needed", in: "one,two", want: []string{"one", "two"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, parseCSV(tt.in))
		})
	}
}
