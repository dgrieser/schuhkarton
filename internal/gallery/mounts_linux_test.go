//go:build linux

package gallery

import "testing"

func TestUnescapeMount(t *testing.T) {
	if got := unescapeMount(`/mnt/Meine\040Bilder`); got != "/mnt/Meine Bilder" {
		t.Errorf("unescapeMount: %q", got)
	}
}
