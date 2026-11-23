package sync

import (
	"path/filepath"
	"testing"
	"time"

	things "github.com/arthursoares/things-cloud-sdk"
)

func TestStateChecklistItem(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	syncer, err := Open(dbPath, nil)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer syncer.Close()

	item := &things.CheckListItem{
		UUID:    "check-state-1",
		Title:   "Lookup me",
		TaskIDs: []string{"task-state-1"},
		Status:  things.TaskStatusPending,
	}
	if err := syncer.saveChecklistItem(item); err != nil {
		t.Fatalf("saveChecklistItem failed: %v", err)
	}

	state := syncer.State()

	t.Run("existing item", func(t *testing.T) {
		got, err := state.ChecklistItem("check-state-1")
		if err != nil {
			t.Fatalf("ChecklistItem failed: %v", err)
		}
		if got == nil || got.Title != "Lookup me" {
			t.Fatalf("unexpected item: %+v", got)
		}
	})

	t.Run("missing item returns nil", func(t *testing.T) {
		got, err := state.ChecklistItem("no-such-item")
		if err != nil {
			t.Fatalf("ChecklistItem failed: %v", err)
		}
		if got != nil {
			t.Fatalf("expected nil for missing item, got: %+v", got)
		}
	})
}

func TestStateAllChecklistItems(t *testing.T) {
	t.Parallel()
	syncer, err := Open(filepath.Join(t.TempDir(), "test.db"), nil)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer syncer.Close()

	created := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	for _, c := range []*things.CheckListItem{
		{UUID: "b", Title: "second", Index: 1, TaskIDs: []string{"T1"}, CreationDate: created},
		{UUID: "a", Title: "first", Index: 0, TaskIDs: []string{"T1"}, CreationDate: created},
		{UUID: "c", Title: "other task", TaskIDs: []string{"T2"}},
		{UUID: "d", Title: "deleted", TaskIDs: []string{"T2"}},
	} {
		if err := syncer.saveChecklistItem(c); err != nil {
			t.Fatal(err)
		}
	}
	if err := syncer.markChecklistItemDeleted("d"); err != nil {
		t.Fatal(err)
	}
	got, err := syncer.State().AllChecklistItems()
	if err != nil {
		t.Fatal(err)
	}
	if len(got["T1"]) != 2 || got["T1"][0].UUID != "a" || got["T1"][1].UUID != "b" || !got["T1"][0].CreationDate.Equal(created) {
		t.Errorf("T1 = %+v", got["T1"])
	}
	if len(got["T2"]) != 1 || got["T2"][0].UUID != "c" {
		t.Errorf("T2 = %+v", got["T2"])
	}
}
