package app

import (
	"testing"

	"github.com/aurimasniekis/go-brother-scan-to-network-server/internal/brscan"
	"github.com/aurimasniekis/go-brother-scan-to-network-server/internal/render"
)

func TestPanelOutput(t *testing.T) {
	cases := []struct {
		panel brscan.PanelSettings
		want  render.Output
		ok    bool
		warn  bool
	}{
		{brscan.PanelSettings{SaveFileType: 0x01}, render.Output{Format: "pdf"}, true, false},
		{brscan.PanelSettings{SaveFileType: 0x01, SplitType: 1}, render.Output{Format: "pdf", Split: 1}, true, false},
		{brscan.PanelSettings{SaveFileType: 0x01, SplitType: 4, SplitPages: 3}, render.Output{Format: "pdf", Split: 3}, true, false},
		{brscan.PanelSettings{SaveFileType: 0x11}, render.Output{Format: "tiff", Split: 1}, true, false},
		{brscan.PanelSettings{SaveFileType: 0x12}, render.Output{Format: "tiff"}, true, false},
		{brscan.PanelSettings{SaveFileType: 0x21}, render.Output{Format: "jpeg", Split: 1}, true, false},
		{brscan.PanelSettings{SaveFileType: 0x22}, render.Output{Format: "png", Split: 1}, true, false},
		{brscan.PanelSettings{SaveFileType: 0x03}, render.Output{Format: "pdf"}, true, true},
		{brscan.PanelSettings{SaveFileType: 0x24}, render.Output{Format: "pdf"}, true, true},
		{brscan.PanelSettings{SaveFileType: 0xff}, render.Output{}, false, false},
		{brscan.PanelSettings{SaveFileType: 0x77}, render.Output{}, false, true},
	}
	for _, c := range cases {
		got, warn, ok := panelOutput(c.panel)
		if got != c.want || ok != c.ok || (warn != "") != c.warn {
			t.Errorf("0x%02x split %d: got %+v ok=%v warn=%q", c.panel.SaveFileType, c.panel.SplitType, got, ok, warn)
		}
	}
}
