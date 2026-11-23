package thingsdav

// Checklist items render as CalDAV subtasks: one VTODO per item, in the same
// calendar as its task, linked by RELATED-TO;RELTYPE=PARENT to the task's
// UID (RFC 5545 §3.8.4.5, the form BusyCal and Apple Reminders nest under).
// Things checklist items hold only a title, a status and a position, so a
// subtask carries no date, tags, notes or alarms.

import (
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-ical"

	things "github.com/arthursoares/things-cloud-sdk"
)

// propSortOrder is the ordering hint BusyCal and Apple clients read for
// subtasks.
const propSortOrder = "X-APPLE-SORT-ORDER"

// ChecklistKey names a checklist item's merge snapshots.
func ChecklistKey(uuid string) string { return "check:" + uuid }

// IsChecklistKey reports whether an Object key names a checklist item.
func IsChecklistKey(key string) bool { return strings.HasPrefix(key, "check:") }

// ChecklistFields is the part of a checklist item CalDAV can see and edit,
// three-way merged like TaskFields.
type ChecklistFields struct {
	Title  string `json:"title"`
	Status string `json:"status"` // StatusOpen, StatusCompleted, StatusCanceled
}

// ChecklistFromItem derives the CalDAV view of a checklist item.
func ChecklistFromItem(c *things.CheckListItem) ChecklistFields {
	f := ChecklistFields{Title: c.Title, Status: StatusOpen}
	switch c.Status {
	case things.TaskStatusCompleted:
		f.Status = StatusCompleted
	case things.TaskStatusCanceled:
		f.Status = StatusCanceled
	}
	return f
}

// ChecklistFromTodo takes the checklist-relevant fields of a parsed VTODO.
func ChecklistFromTodo(p *ParsedTodo) ChecklistFields {
	return ChecklistFields{Title: p.Fields.Title, Status: p.Fields.Status}
}

// MergeChecklist three-way merges a client edit of a subtask.
func MergeChecklist(base, client, current ChecklistFields) (ChecklistFields, []Conflict) {
	var out ChecklistFields
	var conflicts []Conflict
	merge3("title", base.Title, client.Title, current.Title, &out.Title, &conflicts)
	merge3("status", base.Status, client.Status, current.Status, &out.Status, &conflicts)
	return out, conflicts
}

// addChecklist renders a task's checklist items into its calendar.
func (b *builder) addChecklist(c *Calendar, t *things.Task, parentUID string) error {
	for _, item := range b.in.Checklists[t.UUID] {
		if item == nil || item.UUID == "" {
			continue
		}
		f := ChecklistFromItem(item)
		uid, name := item.UUID, item.UUID+".ics"
		if u, n, ok := b.in.Alias(item.UUID); ok {
			uid, name = u, n
		}
		created := item.CreationDate.UTC().Truncate(time.Second)
		if created.IsZero() {
			created = t.CreationDate.UTC().Truncate(time.Second)
		}
		mod := created
		if item.CompletionDate != nil && item.CompletionDate.After(mod) {
			mod = item.CompletionDate.UTC().Truncate(time.Second)
		}
		o := &Object{Name: name, UID: uid, Key: ChecklistKey(item.UUID), TaskUUID: t.UUID,
			ChecklistUUID: item.UUID, Title: item.Title, Created: created, ModTime: mod}
		if err := b.addObject(c, o, wrapCalendar(renderSubtask(item, f, uid, parentUID, created, mod)), f); err != nil {
			return err
		}
	}
	return nil
}

func renderSubtask(item *things.CheckListItem, f ChecklistFields, uid, parentUID string, created, mod time.Time) *ical.Component {
	c := ical.NewComponent(ical.CompToDo)
	c.Props.SetText(ical.PropUID, uid)
	c.Props.SetText(ical.PropSummary, f.Title)
	c.Props.SetDateTime(ical.PropDateTimeStamp, mod)
	if !created.IsZero() {
		c.Props.SetDateTime(ical.PropCreated, created)
	}
	rel := ical.NewProp(ical.PropRelatedTo)
	rel.Value = parentUID
	rel.Params.Set(ical.ParamRelationshipType, "PARENT")
	c.Props.Set(rel)
	order := ical.NewProp(propSortOrder)
	order.Value = strconv.Itoa(item.Index)
	c.Props.Set(order)
	switch f.Status {
	case StatusCompleted:
		c.Props.SetText(ical.PropStatus, "COMPLETED")
		if item.CompletionDate != nil {
			c.Props.SetDateTime(ical.PropCompleted, item.CompletionDate.UTC())
		}
		pct := ical.NewProp(ical.PropPercentComplete)
		pct.SetValueType(ical.ValueInt)
		pct.Value = "100"
		c.Props.Set(pct)
	case StatusCanceled:
		c.Props.SetText(ical.PropStatus, "CANCELLED")
	default:
		c.Props.SetText(ical.PropStatus, "NEEDS-ACTION")
	}
	return c
}

// parentUID returns the UID a VTODO names as its parent: the first
// RELATED-TO whose RELTYPE is PARENT (the default when RELTYPE is absent).
func parentUID(todo *ical.Component) string {
	for _, p := range todo.Props.Values(ical.PropRelatedTo) {
		rt := strings.ToUpper(p.Params.Get(ical.ParamRelationshipType))
		if rt == "" || rt == "PARENT" {
			if v := strings.TrimSpace(p.Value); v != "" {
				return v
			}
		}
	}
	return ""
}
