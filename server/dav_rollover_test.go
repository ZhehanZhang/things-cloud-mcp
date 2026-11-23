package main

import (
	"strings"
	"testing"
	"time"

	things "github.com/arthursoares/things-cloud-sdk"
	"github.com/arthursoares/things-cloud-sdk/thingsdav"
)

// overdueTask adds an open loose task scheduled three days ago (New York
// time, the fake's THINGS_TIMEZONE) with a 09:00 reminder.
func overdueTask(f *fakeDAV, uuid string) (path string, past, today time.Time) {
	ny := thingsLocation()
	now := time.Now().In(ny)
	today = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	past = today.AddDate(0, 0, -3)
	alarm := 9 * 3600
	f.add(&things.Task{UUID: uuid, Title: "carried", Type: things.TaskTypeTask,
		Schedule: things.TaskScheduleAnytime, ScheduledDate: &past, TodayIndexReference: &past,
		AlarmTimeOffset: &alarm})
	return davCalendarPath(thingsdav.NoProjectID) + uuid + ".ics", past, today
}

func TestDAVRolloverServesToday(t *testing.T) {
	f := newFakeDAV(t)
	path, _, today := overdueTask(f, "R1")
	body := f.get("mac", path)
	if !strings.Contains(body, "DUE;VALUE=DATE:"+today.Format("20060102")) || strings.Contains(body, "VALARM") {
		t.Errorf("overdue task not rolled to today:\n%s", body)
	}
}

func TestDAVRolloverEditKeepsThingsSchedule(t *testing.T) {
	f := newFakeDAV(t)
	path, _, _ := overdueTask(f, "R1")
	body := f.get("mac", path)
	f.put("mac", path, replaceLine(body, "SUMMARY:", "SUMMARY:renamed"), nil)

	u := f.onlyUpdate()
	if u["tt"] != "renamed" || len(u) != 2 {
		t.Errorf("update = %+v, want only the title (no date, no reminder change)", u)
	}
}

func TestDAVRolloverCompleteInBusyCal(t *testing.T) {
	f := newFakeDAV(t)
	path, _, _ := overdueTask(f, "R1")
	body := f.get("mac", path)
	f.put("mac", path, addLine(replaceLine(body, "STATUS:", "STATUS:COMPLETED"), "COMPLETED:20261001T113339Z"), nil)

	u := f.onlyUpdate()
	if u["ss"] != 3 {
		t.Errorf("not completed: %+v", u)
	}
	for _, k := range []string{"sr", "st", "ato"} {
		if _, ok := u[k]; ok {
			t.Errorf("completing a rolled task must not touch %s: %+v", k, u)
		}
	}
}

func TestDAVRolloverTimeBlockTodayCommitsDate(t *testing.T) {
	f := newFakeDAV(t)
	path, _, today := overdueTask(f, "R1")
	body := f.get("mac", path)
	at := today.Format("20060102") + "T140000"
	timed := replaceLine(replaceLine(body, "DUE;", "DUE;TZID=America/New_York:"+at), "DTSTART;", "DTSTART;TZID=America/New_York:"+at)
	f.put("mac", path, addLine(timed, "X-BUSYMAC-TASK-DURATION;SHARE-SCOPE=GLOBAL:3600"), nil)

	u := f.onlyUpdate()
	if u["st"] != 1 || u["sr"] != today.Unix() || u["ato"] != 9*3600 {
		t.Errorf("time-blocking a rolled task on today must move it to today in Things, keeping the reminder: %+v", u)
	}

	// Things now has it on today; the block renders there.
	sr := today
	f.tasks["R1"].ScheduledDate, f.tasks["R1"].TodayIndexReference = &sr, &sr
	if got := f.get("mac", path); !strings.Contains(got, "DUE;TZID=America/New_York:"+at) {
		t.Errorf("block not served on today:\n%s", got)
	}
}

func TestDAVRolloverMoveToTomorrow(t *testing.T) {
	f := newFakeDAV(t)
	path, _, today := overdueTask(f, "R1")
	body := f.get("mac", path)
	tom := today.AddDate(0, 0, 1).Format("20060102")
	f.put("mac", path, replaceLine(replaceLine(body, "DUE;", "DUE;VALUE=DATE:"+tom), "DTSTART;", "DTSTART;VALUE=DATE:"+tom), nil)

	u := f.onlyUpdate()
	if u["st"] != 2 || u["sr"] != today.AddDate(0, 0, 1).Unix() || u["ato"] != 9*3600 {
		t.Errorf("drag to tomorrow = %+v, want Upcoming tomorrow with the reminder kept", u)
	}
}
