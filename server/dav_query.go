package main

// go-webdav v0.7's caldav.Filter only evaluates time-range filters on
// VEVENTs: any other component with a time-range fails to match (match.go:
// "TODO handle more than just events"). A calendar-query REPORT for VTODOs
// within a time range therefore came back with zero tasks, and BusyCal
// dropped the whole list until its next ETag-based refresh brought it back.
// davFilter strips VTODO time ranges before handing the query to go-webdav
// and evaluates them itself, per RFC 4791 §9.9.

import (
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav/caldav"
)

type davTimeRange struct{ start, end time.Time }

// davFilter is caldav.Filter with VTODO time ranges supported.
func davFilter(query *caldav.CalendarQuery, objs []caldav.CalendarObject) ([]caldav.CalendarObject, error) {
	if query == nil {
		return objs, nil
	}
	var ranges []davTimeRange
	stripped := *query
	stripped.CompFilter = stripTodoRanges(query.CompFilter, &ranges)
	if len(ranges) == 0 {
		return caldav.Filter(query, objs)
	}
	loc := thingsLocation()
	var out []caldav.CalendarObject
	for _, co := range objs {
		ok, err := caldav.Match(stripped.CompFilter, &co)
		if err != nil {
			return nil, err
		}
		if ok && todosInRanges(co.Data.Component, ranges, loc) {
			out = append(out, co)
		}
	}
	return out, nil
}

// stripTodoRanges returns a copy of f with the time range removed from
// every VTODO comp-filter, appending each removed range to ranges.
func stripTodoRanges(f caldav.CompFilter, ranges *[]davTimeRange) caldav.CompFilter {
	if f.Name == ical.CompToDo && !f.IsNotDefined && (!f.Start.IsZero() || !f.End.IsZero()) {
		*ranges = append(*ranges, davTimeRange{f.Start, f.End})
		f.Start, f.End = time.Time{}, time.Time{}
	}
	if len(f.Comps) > 0 {
		comps := make([]caldav.CompFilter, len(f.Comps))
		for i, c := range f.Comps {
			comps[i] = stripTodoRanges(c, ranges)
		}
		f.Comps = comps
	}
	return f
}

// todosInRanges reports whether, for every range, some VTODO in cal
// overlaps it. Objects here carry one VTODO each, so this is exact for them.
func todosInRanges(cal *ical.Component, ranges []davTimeRange, loc *time.Location) bool {
	for _, r := range ranges {
		found := false
		for _, c := range cal.Children {
			if c.Name == ical.CompToDo && todoInRange(c, r, loc) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// todoInRange applies RFC 4791 §9.9's VTODO table. A zero start or end is
// unbounded on that side.
func todoInRange(todo *ical.Component, r davTimeRange, loc *time.Location) bool {
	prop := func(name string) (time.Time, bool) {
		p := todo.Props.Get(name)
		if p == nil {
			return time.Time{}, false
		}
		t, err := p.DateTime(loc)
		return t, err == nil
	}
	startBefore := func(t time.Time) bool { return r.start.IsZero() || r.start.Before(t) }   // start < t
	startNotAfter := func(t time.Time) bool { return r.start.IsZero() || !r.start.After(t) } // start <= t
	endAfter := func(t time.Time) bool { return r.end.IsZero() || r.end.After(t) }           // end > t
	endNotBefore := func(t time.Time) bool { return r.end.IsZero() || !r.end.Before(t) }     // end >= t

	dtstart, hasStart := prop(ical.PropDateTimeStart)
	due, hasDue := prop(ical.PropDue)
	completed, hasCompleted := prop(ical.PropCompleted)
	created, hasCreated := prop(ical.PropCreated)
	var dur time.Duration
	hasDur := false
	if p := todo.Props.Get(ical.PropDuration); p != nil {
		if d, err := p.Duration(); err == nil {
			dur, hasDur = d, true
		}
	}

	switch {
	case hasStart && hasDur:
		end := dtstart.Add(dur)
		return startNotAfter(end) && (endAfter(dtstart) || endNotBefore(end))
	case hasStart && hasDue:
		return (startBefore(due) || startNotAfter(dtstart)) && (endAfter(dtstart) || endNotBefore(due))
	case hasStart:
		return startNotAfter(dtstart) && endAfter(dtstart)
	case hasDue:
		return startBefore(due) && endNotBefore(due)
	case hasCompleted && hasCreated:
		return (startNotAfter(created) || startNotAfter(completed)) && (endNotBefore(created) || endNotBefore(completed))
	case hasCompleted:
		return startNotAfter(completed) && endNotBefore(completed)
	case hasCreated:
		return endAfter(created)
	default:
		return true
	}
}
