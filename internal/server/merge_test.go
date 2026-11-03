package server

import (
	"testing"

	"github.com/dgrieser/schuhkarton/internal/config"
)

// The settings page sends weekly and one-time windows; they reach the file.
func TestMergeBackgroundWindows(t *testing.T) {
	cur := config.Defaults()
	cur.Background = config.Background{From: "23:00", To: "05:00"}
	in := config.Defaults()
	in.Background = config.Background{
		Windows: []config.BackgroundWindow{{Days: "mon-fri", From: "23:00", To: "08:00"}, {Days: "sat,sun", From: "00:00", To: "07:00"}},
		Once:    []config.BackgroundOnce{{From: "2999-11-03 09:00", To: "13:00"}},
	}
	in.Cache.MaxSize, in.Cache.OverflowDir, in.Media.SlowDecodes = "2GB", "/mnt/nas/cache", 2
	n := merge(cur, in)
	if n.Background.From != "" || len(n.Background.Windows) != 2 || len(n.Background.Once) != 1 {
		t.Errorf("background %+v", n.Background)
	}
	if w := n.Window(); len(w.Rules) != 2 || len(w.Extra) != 1 {
		t.Errorf("window %+v", w)
	}
	if n.Cache.MaxSize != "2GB" || n.Cache.OverflowDir != "/mnt/nas/cache" || n.Media.SlowDecodes != 2 {
		t.Errorf("cache %+v media %d", n.Cache, n.Media.SlowDecodes)
	}
}
