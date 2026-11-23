package main

// CalDAV writes to subtasks, which are Things checklist items (see
// thingsdav/checklist.go). A subtask PUT three-way merges title and status
// like a task PUT; a subtask created under a to-do becomes a checklist item
// on that task; deleting one deletes the checklist item.
//
// BusyCal moves a to-do between lists by deleting and re-creating it with a
// new UID, and does the same to its subtasks. A re-created subtask whose
// CREATED and title match one just deleted over CalDAV, or one still live on
// the parent task, takes that item's place instead of adding a duplicate.

import (
	"fmt"
	"net/http"
	"time"

	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/caldav"

	things "github.com/arthursoares/things-cloud-sdk"
	"github.com/arthursoares/things-cloud-sdk/thingsdav"
)

// davResolveParent maps a subtask's RELATED-TO UID to the Things task it
// belongs to: a rendered to-do with that UID (aliases included), else a
// Things task with that UUID.
func davResolveParent(view *thingsdav.View, uid string) (string, error) {
	for _, c := range view.Calendars {
		if c.Kind != thingsdav.KindTodo {
			continue
		}
		for _, o := range c.Objects {
			if o.ChecklistUUID == "" && o.UID == uid {
				return o.TaskUUID, nil
			}
		}
	}
	t, err := davState().Task(uid)
	if err != nil || t == nil || t.InTrash || t.Type != things.TaskTypeTask {
		return "", nil
	}
	return t.UUID, nil
}

// davChecklistUpdate builds the ChecklistItem3 update from cur to want; nil
// when nothing changed.
func davChecklistUpdate(cur, want thingsdav.ChecklistFields, completedAt *time.Time) map[string]any {
	p := map[string]any{}
	if want.Title != cur.Title && want.Title != "" {
		p["tt"] = want.Title
	}
	if want.Status != cur.Status {
		switch want.Status {
		case thingsdav.StatusCompleted:
			ts := nowTs()
			if completedAt != nil {
				ts = float64(completedAt.UnixNano()) / 1e9
			}
			p["ss"], p["sp"] = 3, ts
		case thingsdav.StatusCanceled:
			p["ss"], p["sp"] = 2, nowTs()
		default:
			p["ss"], p["sp"] = 0, nil
		}
	}
	if len(p) == 0 {
		return nil
	}
	p["md"] = nowTs()
	return p
}

func davWriteChecklist(uuid string, payload map[string]any) error {
	if err := writeToHistory(writeEnvelope{id: uuid, action: 1, kind: "ChecklistItem3", payload: payload}); err != nil {
		return err
	}
	syncAfterWrite()
	return nil
}

// davNextChecklistIndex is one past the last item on a task, so new
// subtasks land at the bottom as they do in BusyCal.
func davNextChecklistIndex(taskUUID string) (int, error) {
	items, err := davState().ChecklistItems(taskUUID)
	if err != nil {
		return 0, err
	}
	next := 0
	for _, it := range items {
		if it.Index >= next {
			next = it.Index + 1
		}
	}
	return next, nil
}

func davUpdateChecklist(device string, view *thingsdav.View, o *thingsdav.Object, parsed *thingsdav.ParsedTodo, opts *caldav.PutCalendarObjectOptions) error {
	item, err := davState().ChecklistItem(o.ChecklistUUID)
	if err != nil || item == nil {
		return davNotFound("checklist item %s no longer exists", o.ChecklistUUID)
	}
	current := thingsdav.ChecklistFromItem(item)
	var base thingsdav.ChecklistFields
	if err := davBase(device, o, opts, current, &base); err != nil {
		return err
	}
	client := thingsdav.ChecklistFromTodo(parsed)
	merged, conflicts := thingsdav.MergeChecklist(base, client, current)
	payload := davChecklistUpdate(current, merged, parsed.CompletedAt)

	// Dragged under another to-do: move the item to that task. Dropping the
	// parent (outdenting) can't turn a checklist item into a task, so the
	// subtask stays where Things has it.
	if parsed.ParentUID != "" {
		dest, err := davResolveParent(view, parsed.ParentUID)
		if err != nil {
			return err
		}
		if dest != "" && dest != o.TaskUUID {
			ix, err := davNextChecklistIndex(dest)
			if err != nil {
				return err
			}
			if payload == nil {
				payload = map[string]any{"md": nowTs()}
			}
			payload["ts"], payload["ix"] = []string{dest}, ix
		}
	}
	if payload != nil {
		if err := davWriteChecklist(item.UUID, payload); err != nil {
			return err
		}
	}
	davSaveChecklistState(device, item.UUID, client)
	davDB.logWrite(device, o.Key, "update", map[string]any{"conflicts": conflicts})
	for _, c := range conflicts {
		logDAV("conflict on checklist %s.%s: client %q, Things %q — Things wins", item.UUID, c.Field, c.Client, c.Things)
	}
	return nil
}

// davSaveChecklistState records what the device now holds as its base.
func davSaveChecklistState(device, itemUUID string, fields thingsdav.ChecklistFields) {
	if err := davDB.setSnapshot(device, thingsdav.ChecklistKey(itemUUID), fields); err != nil {
		logDAV("save snapshot for checklist %s: %v", itemUUID, err)
	}
	davGeneration.Add(1)
}

func davCreateSubtask(device string, view *thingsdav.View, name string, parsed *thingsdav.ParsedTodo) error {
	if parsed.UID == "" {
		return davBadRequest("VTODO has no UID")
	}
	client := thingsdav.ChecklistFromTodo(parsed)
	if client.Title == "" {
		return davBadRequest("subtask has no title")
	}
	parent, err := davResolveParent(view, parsed.ParentUID)
	if err != nil {
		return err
	}

	var recent *davChecklistDelete
	if !parsed.Created.IsZero() {
		if recent, err = davDB.takeChecklistDelete(parsed.Created, client.Title, time.Now().Add(-davMoveWindow)); err != nil {
			return err
		}
	}
	if parent == "" && recent != nil {
		if t, _ := davState().Task(recent.taskUUID); t != nil && !t.InTrash {
			parent = t.UUID
		}
	}
	if parent == "" {
		// Usually the parent is mid-move and its own create hasn't arrived;
		// Things still has these subtasks, so the next sync restores them.
		return webdav.NewHTTPError(http.StatusForbidden,
			fmt.Errorf("subtask's parent to-do %s is not in Things", parsed.ParentUID))
	}

	// The same item still live on the task: adopt the client's new UID.
	if !parsed.Created.IsZero() {
		items, err := davState().ChecklistItems(parent)
		if err != nil {
			return err
		}
		for _, it := range items {
			if it.Title != client.Title {
				continue
			}
			full, err := davState().ChecklistItem(it.UUID)
			if err != nil || full == nil || !full.CreationDate.Truncate(time.Second).Equal(parsed.Created.Truncate(time.Second)) {
				continue
			}
			cur := thingsdav.ChecklistFromItem(full)
			if p := davChecklistUpdate(cur, client, parsed.CompletedAt); p != nil {
				if err := davWriteChecklist(full.UUID, p); err != nil {
					return err
				}
			}
			if err := davDB.setAlias(full.UUID, parsed.UID, name); err != nil {
				logDAV("save alias for checklist %s: %v", full.UUID, err)
			}
			davSaveChecklistState(device, full.UUID, client)
			davDB.logWrite(device, thingsdav.ChecklistKey(full.UUID), "adopt", map[string]string{"task": parent})
			return nil
		}
	}

	cd := nowTs()
	var ix int
	action := "create"
	if recent != nil {
		cd, ix, action = float64(recent.created.Unix()), recent.index, "restore"
	} else if ix, err = davNextChecklistIndex(parent); err != nil {
		return err
	}
	ss := 0
	var sp any
	switch client.Status {
	case thingsdav.StatusCompleted:
		ts := nowTs()
		if parsed.CompletedAt != nil {
			ts = float64(parsed.CompletedAt.UnixNano()) / 1e9
		}
		ss, sp = 3, ts
	case thingsdav.StatusCanceled:
		ss, sp = 2, nowTs()
	}
	uuid := generateUUID()
	payload := map[string]any{
		"tt": client.Title, "ts": []string{parent}, "ix": ix, "cd": cd, "md": nil,
		"ss": ss, "sp": sp, "lt": false, "xx": defaultExtension(),
	}
	if err := writeToHistory(writeEnvelope{id: uuid, action: 0, kind: "ChecklistItem3", payload: payload}); err != nil {
		return err
	}
	syncAfterWrite()
	if err := davDB.setAlias(uuid, parsed.UID, name); err != nil {
		logDAV("save alias for checklist %s: %v", uuid, err)
	}
	davSaveChecklistState(device, uuid, client)
	davDB.logWrite(device, thingsdav.ChecklistKey(uuid), action, map[string]string{"task": parent, "title": client.Title})
	return nil
}

// davDeleteChecklist deletes a checklist item. Things has no trash for
// checklist items, so the delete is remembered for davMoveWindow to let a
// subtask re-created by a list move take the item's place.
func davDeleteChecklist(device string, o *thingsdav.Object) error {
	if err := davAllowChecklistDelete(time.Now()); err != nil {
		return err
	}
	item, err := davState().ChecklistItem(o.ChecklistUUID)
	if err != nil || item == nil {
		return davNotFound("checklist item %s no longer exists", o.ChecklistUUID)
	}
	tomb := map[string]any{"dloid": item.UUID, "dld": nowTs()}
	if err := writeToHistory(writeEnvelope{id: generateUUID(), action: 0, kind: "Tombstone2", payload: tomb}); err != nil {
		return err
	}
	syncAfterWrite()
	if err := davDB.recordChecklistDelete(davChecklistDelete{
		itemUUID: item.UUID, taskUUID: o.TaskUUID, created: o.Created, title: item.Title, index: item.Index,
	}); err != nil {
		logDAV("record checklist delete: %v", err)
	}
	davDB.logWrite(device, o.Key, "delete", map[string]string{"title": item.Title, "task": o.TaskUUID})
	return nil
}
