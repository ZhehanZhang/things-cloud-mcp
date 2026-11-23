package thingsdav

import (
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-ical"

	things "github.com/arthursoares/things-cloud-sdk"
)

func TestRenderChecklistAsSubtasks(t *testing.T) {
	t.Parallel()
	f := newFixture()
	tk := f.add(task("Task1", "Pack"))
	tk.ParentTaskIDs = []string{"Proj1"}
	doneAt := time.Date(2026, 9, 21, 7, 0, 0, 0, time.UTC)
	f.in.Checklists = map[string][]*things.CheckListItem{"Task1": {
		{UUID: "C1", Title: "passport", Index: 0, CreationDate: created, Status: things.TaskStatusCompleted, CompletionDate: &doneAt},
		{UUID: "C2", Title: "charger", Index: 1, CreationDate: created},
	}}
	f.in.Alias = func(uuid string) (string, string, bool) {
		if uuid == "Task1" {
			return "CLIENT-T1", "CLIENT-T1.ics", true
		}
		return "", "", false
	}
	v := build(t, f)
	got := raw(t, v, "project-Proj1", "C1")
	want := `BEGIN:VCALENDAR
PRODID:-//things-cloud-mcp//thingsdav//EN
VERSION:2.0
BEGIN:VTODO
COMPLETED:20260921T070000Z
CREATED:20260901T120000Z
DTSTAMP:20260921T070000Z
PERCENT-COMPLETE:100
RELATED-TO;RELTYPE=PARENT:CLIENT-T1
STATUS:COMPLETED
SUMMARY:passport
UID:C1
X-APPLE-SORT-ORDER:0
END:VTODO
END:VCALENDAR
`
	if got != want {
		t.Errorf("subtask mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	o := v.Calendar("project-Proj1").Object("C2.ics")
	if o.Key != ChecklistKey("C2") || o.TaskUUID != "Task1" || o.ChecklistUUID != "C2" {
		t.Errorf("object = %+v", o)
	}
	if !strings.Contains(string(o.Raw), "STATUS:NEEDS-ACTION") {
		t.Errorf("open item:\n%s", o.Raw)
	}
}

func TestChecklistOfHiddenTaskNotRendered(t *testing.T) {
	t.Parallel()
	f := newFixture()
	tk := f.add(task("Task1", "Trashed"))
	tk.InTrash = true
	f.in.Checklists = map[string][]*things.CheckListItem{"Task1": {{UUID: "C1", Title: "x"}}}
	for _, c := range build(t, f).Calendars {
		if c.Object("C1.ics") != nil {
			t.Fatalf("subtask of a trashed task rendered in %s", c.ID)
		}
	}
}

func TestParseTodoParentAndOwnedRelation(t *testing.T) {
	t.Parallel()
	src := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:x\r\nBEGIN:VTODO\r\nUID:S1\r\nDTSTAMP:20261001T120000Z\r\n" +
		"SUMMARY:sub\r\nRELATED-TO;RELTYPE=SIBLING:OTHER\r\nRELATED-TO:P1\r\nX-APPLE-SORT-ORDER:5\r\nEND:VTODO\r\nEND:VCALENDAR\r\n"
	cal, err := ical.NewDecoder(strings.NewReader(src)).Decode()
	if err != nil {
		t.Fatal(err)
	}
	p, err := ParseTodo(cal, "", time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if p.ParentUID != "P1" {
		t.Errorf("ParentUID = %q", p.ParentUID)
	}
	if p.Extras != nil {
		s, _ := EncodeExtras(p.Extras)
		t.Errorf("nesting props must not land in the sidecar:\n%s", s)
	}
}

func TestMergeChecklist(t *testing.T) {
	t.Parallel()
	base := ChecklistFields{Title: "a", Status: StatusOpen}
	got, conflicts := MergeChecklist(base, ChecklistFields{Title: "a", Status: StatusCompleted}, ChecklistFields{Title: "b", Status: StatusOpen})
	if got != (ChecklistFields{Title: "b", Status: StatusCompleted}) || len(conflicts) != 0 {
		t.Errorf("merge = %+v %v", got, conflicts)
	}
	got, conflicts = MergeChecklist(base, ChecklistFields{Title: "c", Status: StatusOpen}, ChecklistFields{Title: "b", Status: StatusOpen})
	if got.Title != "b" || len(conflicts) != 1 {
		t.Errorf("conflict merge = %+v %v", got, conflicts)
	}
}
