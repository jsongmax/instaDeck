package httpapi

import (
	"testing"

	"instadeck/internal/instabox"
)

func TestLatestTemplatesKeepsOnePerID(t *testing.T) {
	got := latestTemplates([]instabox.Template{
		{ID: "ci", Version: "1", Creatable: true},
		{ID: "browser", Version: "1", Creatable: true, Default: true},
		{ID: "browser", Version: "3", Creatable: true, Latest: true},
		{ID: "browser", Version: "2", Creatable: true},
		{ID: "ci", Version: "2", Creatable: true},
		{ID: "old", Version: "1", Creatable: true},
		{ID: "old", Version: "2", Creatable: false},
	})
	if len(got) != 3 {
		t.Fatalf("got %d entries: %+v", len(got), got)
	}
	// Order follows first appearance.
	if got[0].ID != "ci" || got[1].ID != "browser" || got[2].ID != "old" {
		t.Fatalf("order: %+v", got)
	}
	// Without a latest mark, the last listed version wins.
	if got[0].Version != "2" {
		t.Errorf("ci version %s", got[0].Version)
	}
	// A later entry does not replace the one marked latest, and default survives.
	if got[1].Version != "3" || !got[1].Default {
		t.Errorf("browser: %+v", got[1])
	}
	// A retired last version stays not creatable, as upstream would refuse it.
	if got[2].Version != "2" || got[2].Creatable {
		t.Errorf("old: %+v", got[2])
	}
}
