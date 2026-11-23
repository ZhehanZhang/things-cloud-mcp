package main

import (
	"net/http"
	"strings"
	"testing"
	"time"

	things "github.com/arthursoares/things-cloud-sdk"
	"github.com/arthursoares/things-cloud-sdk/thingsdav"
)

var checkCreated = time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)

// taskWithChecklist adds an Inbox task T1 with items C1 ("milk") and C2
// ("eggs") and returns the subtask path of C1.
func taskWithChecklist(f *fakeDAV) string {
	inboxTask(f, "T1", "groceries")
	f.checks["T1"] = []*things.CheckListItem{
		{UUID: "C1", Title: "milk", Index: 0, CreationDate: checkCreated, TaskIDs: []string{"T1"}},
		{UUID: "C2", Title: "eggs", Index: 1, CreationDate: checkCreated.Add(time.Minute), TaskIDs: []string{"T1"}},
	}
	return davCalendarPath(thingsdav.InboxID) + "C1.ics"
}

func (f *fakeDAV) onlyWrite(kind string, action int) writeEnvelope {
	f.t.Helper()
	if len(f.writes) != 1 || f.writes[0].kind != kind || f.writes[0].action != action {
		f.t.Fatalf("want one %s action %d, got %+v", kind, action, f.writes)
	}
	return f.writes[0]
}

func TestDAVSubtaskRendersUnderTask(t *testing.T) {
	f := newFakeDAV(t)
	path := taskWithChecklist(f)
	body := f.get("mac", path)
	for _, want := range []string{"RELATED-TO;RELTYPE=PARENT:T1\r\n", "SUMMARY:milk\r\n", "STATUS:NEEDS-ACTION\r\n"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in\n%s", want, body)
		}
	}
	if strings.Contains(body, "DUE") {
		t.Errorf("subtasks carry no date:\n%s", body)
	}
}

func TestDAVSubtaskCompleteAndRename(t *testing.T) {
	f := newFakeDAV(t)
	path := taskWithChecklist(f)
	body := f.get("mac", path)
	done := addLine(replaceLine(body, "STATUS:", "STATUS:COMPLETED"), "COMPLETED:20261001T113339Z")
	f.put("mac", path, done, nil)
	p := f.onlyWrite("ChecklistItem3", 1).payload.(map[string]any)
	if f.writes[0].id != "C1" || p["ss"] != 3 || p["sp"] != float64(time.Date(2026, 10, 1, 11, 33, 39, 0, time.UTC).Unix()) {
		t.Errorf("complete = %s %+v", f.writes[0].id, p)
	}
	if _, ok := p["tt"]; ok {
		t.Errorf("title untouched: %+v", p)
	}

	f.writes = nil
	f.checks["T1"][0].Status = things.TaskStatusCompleted
	f.put("mac", path, replaceLine(done, "SUMMARY:", "SUMMARY:oat milk"), nil)
	p = f.onlyWrite("ChecklistItem3", 1).payload.(map[string]any)
	if p["tt"] != "oat milk" || p["ss"] != nil {
		t.Errorf("rename = %+v", p)
	}
}

func TestDAVSubtaskConflictThingsWins(t *testing.T) {
	f := newFakeDAV(t)
	path := taskWithChecklist(f)
	body := f.get("mac", path)
	f.checks["T1"][0].Title = "things title"
	f.put("mac", path, replaceLine(body, "SUMMARY:", "SUMMARY:client title"), nil)
	if len(f.writes) != 0 {
		t.Errorf("conflicting title must not be written: %+v", f.writes)
	}
}

func TestDAVSubtaskCreate(t *testing.T) {
	f := newFakeDAV(t)
	taskWithChecklist(f)
	ics := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//BusyMac//EN\r\nBEGIN:VTODO\r\nUID:SUB-1\r\nDTSTAMP:20261001T120000Z\r\n" +
		"SUMMARY:bread\r\nRELATED-TO;RELTYPE=PARENT:T1\r\nEND:VTODO\r\nEND:VCALENDAR\r\n"
	path := davCalendarPath(thingsdav.InboxID) + "SUB-1.ics"
	f.put("mac", path, ics, map[string]string{"If-None-Match": "*"})
	w := f.onlyWrite("ChecklistItem3", 0)
	p := w.payload.(map[string]any)
	if p["tt"] != "bread" || p["ts"].([]string)[0] != "T1" || p["ix"] != 2 || p["ss"] != 0 {
		t.Errorf("create = %+v", p)
	}

	// The client keeps its UID once Things has the item.
	f.checks["T1"] = append(f.checks["T1"], &things.CheckListItem{UUID: w.id, Title: "bread", Index: 2, TaskIDs: []string{"T1"}})
	if got := f.get("mac", path); !strings.Contains(got, "UID:SUB-1\r\n") {
		t.Errorf("client UID not kept:\n%s", got)
	}
}

func TestDAVSubtaskUnknownParentRefused(t *testing.T) {
	f := newFakeDAV(t)
	ics := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:x\r\nBEGIN:VTODO\r\nUID:SUB-2\r\nDTSTAMP:20261001T120000Z\r\n" +
		"SUMMARY:orphan\r\nRELATED-TO:NOPE\r\nEND:VTODO\r\nEND:VCALENDAR\r\n"
	code, _ := f.do(http.MethodPut, "mac", davCalendarPath(thingsdav.InboxID)+"SUB-2.ics", ics, nil)
	if code != http.StatusForbidden || len(f.writes) != 0 {
		t.Errorf("PUT = %d, writes %+v", code, f.writes)
	}
}

func TestDAVSubtaskDeleteThenRecreateRestores(t *testing.T) {
	f := newFakeDAV(t)
	path := taskWithChecklist(f)
	c2 := davCalendarPath(thingsdav.InboxID) + "C2.ics"
	body := f.get("mac", c2)
	if code, _ := f.do(http.MethodDelete, "mac", c2, "", nil); code != http.StatusNoContent {
		t.Fatalf("DELETE = %d", code)
	}
	if w := f.onlyWrite("Tombstone2", 0); w.payload.(map[string]any)["dloid"] != "C2" {
		t.Errorf("tombstone = %+v", w)
	}
	f.writes = nil
	f.checks["T1"] = f.checks["T1"][:1]
	_ = path

	// BusyCal re-creates it (a list move) with a new UID, same CREATED + title.
	moved := replaceLine(body, "UID:", "UID:NEW-SUB")
	f.put("mac", davCalendarPath(thingsdav.InboxID)+"NEW-SUB.ics", moved, map[string]string{"If-None-Match": "*"})
	p := f.onlyWrite("ChecklistItem3", 0).payload.(map[string]any)
	if p["tt"] != "eggs" || p["ix"] != 1 || p["cd"] != float64(checkCreated.Add(time.Minute).Unix()) {
		t.Errorf("restore = %+v", p)
	}
}

func TestDAVSubtaskRecreateOfLiveItemAdopts(t *testing.T) {
	f := newFakeDAV(t)
	path := taskWithChecklist(f)
	body := f.get("mac", path)
	// The parent moved first; the subtask arrives as a new object while the
	// item is still on the task: no duplicate, just a new UID.
	f.put("mac", davCalendarPath(thingsdav.InboxID)+"NEW-C1.ics", replaceLine(body, "UID:", "UID:NEW-C1"),
		map[string]string{"If-None-Match": "*"})
	if len(f.writes) != 0 {
		t.Fatalf("adopting must not write: %+v", f.writes)
	}
	if got := f.get("mac", davCalendarPath(thingsdav.InboxID)+"NEW-C1.ics"); !strings.Contains(got, "SUMMARY:milk") {
		t.Errorf("adopted subtask not served under its new name:\n%s", got)
	}
}

func TestDAVSubtaskReparent(t *testing.T) {
	f := newFakeDAV(t)
	path := taskWithChecklist(f)
	inboxTask(f, "T2", "other")
	body := f.get("mac", path)
	f.put("mac", path, replaceLine(body, "RELATED-TO", "RELATED-TO;RELTYPE=PARENT:T2"), nil)
	p := f.onlyWrite("ChecklistItem3", 1).payload.(map[string]any)
	if p["ts"].([]string)[0] != "T2" || p["ix"] != 0 {
		t.Errorf("reparent = %+v", p)
	}
}

func TestDAVTaskNestingIgnored(t *testing.T) {
	f := newFakeDAV(t)
	path := inboxTask(f, "T1", "parent")
	inboxTask(f, "T2", "child")
	body := f.get("mac", davCalendarPath(thingsdav.InboxID)+"T2.ics")
	f.put("mac", davCalendarPath(thingsdav.InboxID)+"T2.ics", addLine(body, "RELATED-TO;RELTYPE=PARENT:T1"), nil)
	if len(f.writes) != 0 {
		t.Errorf("nesting a task must not write: %+v", f.writes)
	}
	if got := f.get("mac", davCalendarPath(thingsdav.InboxID)+"T2.ics"); strings.Contains(got, "RELATED-TO") {
		t.Errorf("tasks never render nested:\n%s", got)
	}
	_ = path
}
