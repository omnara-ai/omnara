package httpapi

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/omnara-ai/omnara/internal/storage/identitystore"
)

func TestMostRecentlyActiveProjects(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	project := func(updatedAt time.Time) identitystore.VisibleProjectRecord {
		return identitystore.VisibleProjectRecord{
			Project: identitystore.ProjectRecord{ID: uuid.New(), UpdatedAt: updatedAt},
		}
	}
	idle := project(base)
	recentlyUpdated := project(base.Add(2 * time.Hour))
	recentlyActive := project(base)
	activity := map[uuid.UUID]time.Time{
		recentlyActive.Project.ID: base.Add(3 * time.Hour),
		// Activity older than the project's own update doesn't pull it back.
		recentlyUpdated.Project.ID: base.Add(time.Hour),
	}
	visible := []identitystore.VisibleProjectRecord{idle, recentlyUpdated, recentlyActive}

	got := mostRecentlyActiveProjects(visible, activity, 2)
	if len(got) != 2 || got[0].Project.ID != recentlyActive.Project.ID ||
		got[1].Project.ID != recentlyUpdated.Project.ID {
		t.Fatalf("mostRecentlyActiveProjects = %+v, want the active then the updated project", got)
	}
	if all := mostRecentlyActiveProjects(visible, activity, 10); len(all) != 3 {
		t.Fatalf("mostRecentlyActiveProjects with room for all = %d projects, want 3", len(all))
	}
	if visible[0].Project.ID != idle.Project.ID {
		t.Fatal("mostRecentlyActiveProjects reordered its input")
	}
}
