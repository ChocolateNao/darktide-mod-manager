package commands

import (
	"testing"

	"github.com/ChocolateNao/darktide-mod-manager/pkg/api"
	"github.com/stretchr/testify/assert"
)

func TestPageBounds(t *testing.T) {
	tests := []struct {
		name           string
		total          int
		page, pageSize int
		wantStart      int
		wantEnd        int
		wantPages      int
	}{
		{name: "single page", total: 10, page: 1, pageSize: 50, wantStart: 0, wantEnd: 10, wantPages: 1},
		{name: "second page", total: 120, page: 2, pageSize: 50, wantStart: 50, wantEnd: 100, wantPages: 3},
		{name: "last page is short", total: 120, page: 3, pageSize: 50, wantStart: 100, wantEnd: 120, wantPages: 3},
		{name: "page size zero prints all", total: 120, page: 1, pageSize: 0, wantStart: 0, wantEnd: 120, wantPages: 1},
		{name: "page out of range", total: 120, page: 4, pageSize: 50, wantStart: -1, wantEnd: -1, wantPages: 3},
		{name: "page below one", total: 120, page: 0, pageSize: 50, wantStart: -1, wantEnd: -1, wantPages: 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start, end, pages := pageBounds(tt.total, tt.page, tt.pageSize)
			assert.Equal(t, tt.wantStart, start)
			assert.Equal(t, tt.wantEnd, end)
			assert.Equal(t, tt.wantPages, pages)
		})
	}
}

func TestModStateMarksExternalMods(t *testing.T) {
	assert.Equal(t, "external", modState(api.Mod{External: true, Enabled: true}))
	assert.Equal(t, "external, disabled", modState(api.Mod{External: true}))
	assert.Equal(t, "enabled", modState(api.Mod{Enabled: true}))
	assert.Equal(t, "disabled", modState(api.Mod{}))
}
