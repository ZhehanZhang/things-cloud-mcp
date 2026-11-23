package sync

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	things "github.com/arthursoares/things-cloud-sdk"
)

// A repeat instance's template link (rt) must survive the mirror so writes can
// tell an instance from its template.
func TestTaskStorageRepeatingTemplate(t *testing.T) {
	t.Parallel()
	syncer, err := Open(filepath.Join(t.TempDir(), "test.db"), nil)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer syncer.Close()

	task := &things.Task{
		UUID:          "instance",
		Title:         "Weekly chore",
		Type:          things.TaskTypeTask,
		Schedule:      things.TaskScheduleAnytime,
		CreationDate:  time.Now().Truncate(time.Second),
		RecurrenceIDs: []string{"template"},
	}
	if err := syncer.saveTask(task); err != nil {
		t.Fatalf("saveTask failed: %v", err)
	}
	got, err := syncer.getTask("instance")
	if err != nil || got == nil {
		t.Fatalf("getTask = %v, %v", got, err)
	}
	if len(got.RecurrenceIDs) != 1 || got.RecurrenceIDs[0] != "template" {
		t.Fatalf("RecurrenceIDs = %v, want [template]", got.RecurrenceIDs)
	}

	task.RecurrenceIDs = nil
	if err := syncer.saveTask(task); err != nil {
		t.Fatalf("saveTask failed: %v", err)
	}
	got, _ = syncer.getTask("instance")
	if len(got.RecurrenceIDs) != 0 {
		t.Fatalf("RecurrenceIDs = %v after clearing, want none", got.RecurrenceIDs)
	}
}

// Things marks "repeat forever" with an end date of 4001-01-01. The mirror
// must keep it as such rather than wrapping it into 2247-05-05.
func TestTaskStorageNeverendingRepeatSurvives(t *testing.T) {
	t.Parallel()
	syncer, err := Open(filepath.Join(t.TempDir(), "test.db"), nil)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer syncer.Close()

	var rc things.RepeaterConfiguration
	raw := `{"ia":1785628800,"rrv":4,"tp":0,"fu":256,"sr":1785628800,"rc":0,"fa":1,"of":[{"wd":0}],"ts":0,"ed":64092211200}`
	if err := json.Unmarshal([]byte(raw), &rc); err != nil {
		t.Fatal(err)
	}
	task := &things.Task{UUID: "tmpl", Title: "t", Type: things.TaskTypeTask, Schedule: things.TaskScheduleSomeday, CreationDate: time.Now(), Repeater: &rc}
	if err := syncer.saveTask(task); err != nil {
		t.Fatalf("saveTask failed: %v", err)
	}
	got, err := syncer.getTask("tmpl")
	if err != nil || got == nil || got.Repeater == nil {
		t.Fatalf("getTask = %v, %v", got, err)
	}
	if !got.Repeater.IsNeverending() {
		t.Fatalf("end date = %v, want year %d", got.Repeater.LastScheduledAt.Time(), things.NeverendingRepeatYear)
	}
}
