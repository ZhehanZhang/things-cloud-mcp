package thingsdav

import (
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-ical"
)

// now is 2026-10-01 15:00 UTC (see view_test.go).

func TestRolloverMovesOverdueTaskToToday(t *testing.T) {
	t.Parallel()
	f := newFixture()
	tk := task("Late", "carried over")
	tk.ScheduledDate = day(2026, 9, 28)
	tk.TodayIndexReference = day(2026, 9, 28)
	tk.AlarmTimeOffset = intPtr(9 * 3600)
	f.add(tk)

	v := build(t, f)
	got := raw(t, v, NoProjectID, "Late")
	for _, want := range []string{"DUE;VALUE=DATE:20261001", "DTSTART;VALUE=DATE:20261001"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "VALARM") {
		t.Errorf("a rolled task must not re-fire its past reminder:\n%s", got)
	}
	if snap := v.Calendar(NoProjectID).Object("Late.ics").Snapshot; !strings.Contains(snap, `"when":"2026-10-01"`) {
		t.Errorf("snapshot must hold the rolled date, got %s", snap)
	}
}

func TestRolloverLeavesOtherTasksAlone(t *testing.T) {
	t.Parallel()
	f := newFixture()
	done := task("Done", "finished late")
	done.ScheduledDate = day(2026, 9, 28)
	done.Status = 3
	done.CompletionDate = &modified
	f.add(done)

	future := task("Future", "next week")
	future.ScheduledDate = day(2026, 10, 8)
	future.Schedule = 2
	f.add(future)

	todays := task("Today", "on time")
	todays.ScheduledDate = day(2026, 10, 1)
	f.add(todays)

	v := build(t, f)
	for uid, want := range map[string]string{"Done": "20260928", "Future": "20261008", "Today": "20261001"} {
		if got := raw(t, v, NoProjectID, uid); !strings.Contains(got, "DUE;VALUE=DATE:"+want) {
			t.Errorf("%s: want DUE %s:\n%s", uid, want, got)
		}
	}
}

func TestRolloverKeepsPastTimeBlocks(t *testing.T) {
	t.Parallel()
	f := newFixture()
	tk := task("Blocked", "deep work")
	tk.ScheduledDate = day(2026, 9, 29)
	f.add(tk)
	f.in.Extras = func(uuid string) *ical.Calendar {
		if uuid != "Blocked" {
			return nil
		}
		x := newExtras()
		todo := firstChild(x.Component, ical.CompToDo)
		tp := ical.NewProp(propTime)
		tp.Value = "140000"
		tp.Params.Set(ical.PropTimezoneID, "America/New_York")
		todo.Props.Set(tp)
		todo.Props.SetText("X-BUSYMAC-TASK-DURATION", "3600")
		return x
	}

	got := raw(t, build(t, f), NoProjectID, "Blocked")
	if !strings.Contains(got, "DUE;TZID=America/New_York:20260929T140000") {
		t.Errorf("time block must stay on its original day:\n%s", got)
	}
}

func TestRolloverUsesLocalDay(t *testing.T) {
	t.Parallel()
	// 02:00 UTC on Oct 2 is still Oct 1 in New York.
	late := time.Date(2026, 10, 2, 2, 0, 0, 0, time.UTC)
	ny, _ := time.LoadLocation("America/New_York")
	f := TaskFields{Status: StatusOpen, When: "2026-10-01", Reminder: 3600}
	if _, rolled := Rollover(f, false, LocalDay(late, ny)); rolled {
		t.Error("task due today in New York must not roll")
	}
	got, rolled := Rollover(f, false, LocalDay(late, time.UTC))
	if !rolled || got.When != "2026-10-02" || got.Reminder != NoReminder {
		t.Errorf("Rollover in UTC = %+v, %v", got, rolled)
	}
}
