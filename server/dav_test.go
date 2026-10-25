package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/caldav"

	things "github.com/arthursoares/things-cloud-sdk"
	"github.com/arthursoares/things-cloud-sdk/thingsdav"
)

const testDAVPassword = "test-dav-password"

// stubDAV serves a fixed Things state through the full CalDAV stack.
func stubDAV(t *testing.T) *httptest.Server {
	t.Helper()
	mod := time.Date(2026, 9, 20, 8, 30, 0, 0, time.UTC)
	when := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	deadline := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	project := &things.Task{UUID: "Proj1", Title: "Launch", Type: things.TaskTypeProject, AreaIDs: []string{"Area1"}, ModificationDate: &mod}
	view, err := thingsdav.Build(thingsdav.Input{
		Tasks: []*things.Task{
			{UUID: "Task1", Title: "Write report", ParentTaskIDs: []string{"Proj1"}, Schedule: things.TaskScheduleAnytime,
				ScheduledDate: &when, DeadlineDate: &deadline, ModificationDate: &mod},
			{UUID: "Task2", Title: "Inbox item", Schedule: things.TaskScheduleInbox, ModificationDate: &mod},
		},
		Projects: []*things.Task{project},
		Areas:    []*things.Area{{UUID: "Area1", Title: "Work"}},
		Lookup: func(id string) *things.Task {
			if id == "Proj1" {
				return project
			}
			return nil
		},
		Now: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	orig := loadDAVView
	loadDAVView = func() (*thingsdav.View, error) { return view, nil }
	t.Cleanup(func() { loadDAVView = orig })
	t.Setenv("CALDAV_PASSWORD", testDAVPassword)

	mux := http.NewServeMux()
	h := newDAVHandler()
	mux.Handle(davPrefix+"/", h)
	mux.Handle("/.well-known/caldav", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func davClient(t *testing.T, srv *httptest.Server, user, pass string) *caldav.Client {
	t.Helper()
	c, err := caldav.NewClient(webdav.HTTPClientWithBasicAuth(srv.Client(), user, pass), srv.URL+davPrefix+"/")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestDAVDiscoveryAndListing(t *testing.T) {
	srv := stubDAV(t)
	c := davClient(t, srv, "mac", testDAVPassword)
	ctx := context.Background()

	principal, err := c.FindCurrentUserPrincipal(ctx)
	if err != nil || principal != davPrincipalPath {
		t.Fatalf("principal = %q, %v", principal, err)
	}
	home, err := c.FindCalendarHomeSet(ctx, principal)
	if err != nil || home != davHomeSetPath {
		t.Fatalf("home set = %q, %v", home, err)
	}
	cals, err := c.FindCalendars(ctx, home)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, cal := range cals {
		got[cal.Path] = cal.Name + "/" + strings.Join(cal.SupportedComponentSet, ",")
	}
	want := map[string]string{
		davCalendarPath("inbox"):         "Inbox/VTODO",
		davCalendarPath("no-project"):    "Things/VTODO",
		davCalendarPath("area-Area1"):    "Work/VTODO",
		davCalendarPath("project-Proj1"): "Work › Launch/VTODO",
		davCalendarPath("deadlines"):     "Deadlines/VEVENT",
	}
	for path, w := range want {
		if got[path] != w {
			t.Errorf("calendar %s = %q, want %q", path, got[path], w)
		}
	}
	if len(got) != len(want) {
		t.Errorf("got %d calendars, want %d: %v", len(got), len(want), got)
	}

	obj, err := c.GetCalendarObject(ctx, davCalendarPath("project-Proj1")+"Task1.ics")
	if err != nil {
		t.Fatal(err)
	}
	todo := obj.Data.Children[0]
	if todo.Name != ical.CompToDo {
		t.Fatalf("component = %s", todo.Name)
	}
	if due := todo.Props.Get(ical.PropDue); due == nil || due.Value != "20261002" {
		t.Errorf("DUE = %+v", due)
	}
	if obj.ETag == "" {
		t.Error("missing ETag")
	}

	events, err := c.QueryCalendar(ctx, davCalendarPath("deadlines"), &caldav.CalendarQuery{
		CompRequest: caldav.CalendarCompRequest{Name: ical.CompCalendar, AllProps: true, AllComps: true},
		CompFilter:  caldav.CompFilter{Name: ical.CompCalendar, Comps: []caldav.CompFilter{{Name: ical.CompEvent}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Data.Children[0].Props.Get(ical.PropUID).Value != "deadline-Task1" {
		t.Errorf("deadline events = %+v", events)
	}
}

func TestDAVWellKnownRedirect(t *testing.T) {
	srv := stubDAV(t)
	client := srv.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, _ := http.NewRequest("PROPFIND", srv.URL+"/.well-known/caldav", nil)
	req.SetBasicAuth("iphone", testDAVPassword)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusPermanentRedirect || resp.Header.Get("Location") != davPrincipalPath {
		t.Errorf("well-known: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
}

func TestDAVAuth(t *testing.T) {
	srv := stubDAV(t)
	for _, tc := range []struct {
		name       string
		user, pass string
		setAuth    bool
		want       int
	}{
		{"no credentials", "", "", false, http.StatusUnauthorized},
		{"wrong password", "mac", "nope", true, http.StatusUnauthorized},
		{"empty username", "", testDAVPassword, true, http.StatusUnauthorized},
		{"any device name", "kitchen-ipad", testDAVPassword, true, http.StatusMultiStatus},
	} {
		body := `<?xml version="1.0"?><propfind xmlns="DAV:"><prop><resourcetype/></prop></propfind>`
		req, _ := http.NewRequest("PROPFIND", srv.URL+davPrincipalPath, strings.NewReader(body))
		req.Header.Set("Depth", "0")
		req.Header.Set("Content-Type", "application/xml")
		if tc.setAuth {
			req.SetBasicAuth(tc.user, tc.pass)
		}
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Errorf("%s: status %d, want %d", tc.name, resp.StatusCode, tc.want)
		}
		if tc.want == http.StatusUnauthorized && resp.Header.Get("WWW-Authenticate") == "" {
			t.Errorf("%s: missing WWW-Authenticate challenge", tc.name)
		}
	}
}

func TestDAVReadOnly(t *testing.T) {
	srv := stubDAV(t)
	ics := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:test\r\nBEGIN:VTODO\r\nUID:Task2\r\nDTSTAMP:20261001T000000Z\r\nSUMMARY:edited\r\nEND:VTODO\r\nEND:VCALENDAR\r\n"
	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		req, _ := http.NewRequest(method, srv.URL+davCalendarPath("inbox")+"Task2.ics", strings.NewReader(ics))
		req.Header.Set("Content-Type", "text/calendar")
		req.SetBasicAuth("mac", testDAVPassword)
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s: status %d, want 403", method, resp.StatusCode)
		}
	}
}

func TestDAVDisabledWithoutPassword(t *testing.T) {
	t.Setenv("CALDAV_PASSWORD", "")
	t.Setenv("API_KEY", "")
	if newDAVHandler() != nil {
		t.Error("CalDAV must stay disabled without a password")
	}
	t.Setenv("API_KEY", "fallback")
	if davPassword() != "fallback" {
		t.Error("API_KEY should be the fallback password")
	}
}

func TestDAVCompletedDays(t *testing.T) {
	for raw, want := range map[string]int{"": 30, "0": 0, "90": 90, "-1": 30, "abc": 30} {
		t.Setenv("CALDAV_COMPLETED_DAYS", raw)
		if got := davCompletedDays(); got != want {
			t.Errorf("CALDAV_COMPLETED_DAYS=%q → %d, want %d", raw, got, want)
		}
	}
}

// BusyCal asks the server root for current-user-principal after following
// the .well-known redirect (captured in the spike); main routes PROPFIND /
// to the CalDAV handler, which must answer it.
func TestDAVRootPrincipal(t *testing.T) {
	stubDAV(t)
	body := `<?xml version="1.0" encoding="utf-8"?><d:propfind xmlns:d="DAV:"><d:prop><d:current-user-principal/></d:prop></d:propfind>`
	req := httptest.NewRequest("PROPFIND", "/", strings.NewReader(body))
	req.Header.Set("Depth", "0")
	req.Header.Set("Content-Type", "text/xml; charset=UTF-8")
	req.SetBasicAuth("mac", testDAVPassword)
	rec := httptest.NewRecorder()
	newDAVHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusMultiStatus || !strings.Contains(rec.Body.String(), "<href>"+davPrincipalPath+"</href>") {
		t.Errorf("root PROPFIND: %d %s", rec.Code, rec.Body.String())
	}
}

// BusyCal's real calendar-listing PROPFIND (captured in the spike): colors
// and CTags must come back as 200 properties, not 404s.
func TestDAVCalendarColorAndCTag(t *testing.T) {
	srv := stubDAV(t)
	body := `<?xml version="1.0" encoding="utf-8"?><d:propfind xmlns:d="DAV:" xmlns:a="urn:ietf:params:xml:ns:caldav" xmlns:b="http://calendarserver.org/ns/" xmlns:c="http://apple.com/ns/ical/"><d:prop><d:displayname/><d:resourcetype/><d:sync-token/><a:supported-calendar-component-set/><b:getctag/><c:calendar-color/></d:prop></d:propfind>`
	req, _ := http.NewRequest("PROPFIND", srv.URL+davHomeSetPath, strings.NewReader(body))
	req.Header.Set("Depth", "1")
	req.Header.Set("Content-Type", "text/xml; charset=UTF-8")
	req.SetBasicAuth("mac", testDAVPassword)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf strings.Builder
	if _, err := io.Copy(&buf, resp.Body); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("status %d: %s", resp.StatusCode, out)
	}

	view, _ := loadDAVView()
	for _, cal := range view.Calendars {
		block := davResponseBlock(t, out, davCalendarPath(cal.ID))
		wantColor := `<calendar-color xmlns="http://apple.com/ns/ical/">` + cal.Color + `</calendar-color>`
		wantCTag := `<getctag xmlns="http://calendarserver.org/ns/">` + cal.CTag + `</getctag>`
		if !strings.Contains(block, wantColor) || !strings.Contains(block, wantCTag) {
			t.Errorf("%s: missing color/ctag in %s", cal.ID, block)
		}
		if strings.Contains(block, davEmptyColor) || strings.Contains(block, davEmptyCTag) {
			t.Errorf("%s: color/ctag still reported as 404: %s", cal.ID, block)
		}
	}
	// The home set itself is not a calendar and keeps go-webdav's answer.
	if home := davResponseBlock(t, out, davHomeSetPath); !strings.Contains(home, davEmptyColor) {
		t.Errorf("home set should still report calendar-color as 404: %s", home)
	}
	if got := resp.Header.Get("Content-Length"); got != strconv.Itoa(len(out)) {
		t.Errorf("Content-Length %s, body %d bytes", got, len(out))
	}
}

func davResponseBlock(t *testing.T, multistatus, href string) string {
	t.Helper()
	start := strings.Index(multistatus, davResponseOp+"<href>"+href+"</href>")
	if start < 0 {
		t.Fatalf("no response for %s in %s", href, multistatus)
	}
	end := strings.Index(multistatus[start:], "</response>")
	return multistatus[start : start+end]
}

// TestDAVQueryTodoTimeRange: go-webdav alone rejects every VTODO under a
// time-range filter, which emptied BusyCal's task lists.
func TestDAVQueryTodoTimeRange(t *testing.T) {
	srv := stubDAV(t)
	c := davClient(t, srv, "mac", testDAVPassword)
	ctx := context.Background()

	query := func(cal string, start, end time.Time) []string {
		t.Helper()
		objs, err := c.QueryCalendar(ctx, davCalendarPath(cal), &caldav.CalendarQuery{
			CompRequest: caldav.CalendarCompRequest{Name: ical.CompCalendar, AllProps: true, AllComps: true},
			CompFilter: caldav.CompFilter{Name: ical.CompCalendar, Comps: []caldav.CompFilter{
				{Name: ical.CompToDo, Start: start, End: end},
			}},
		})
		if err != nil {
			t.Fatalf("query %s: %v", cal, err)
		}
		var names []string
		for _, o := range objs {
			names = append(names, o.Path[strings.LastIndex(o.Path, "/")+1:])
		}
		return names
	}

	oct := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	if got := query("project-Proj1", oct, oct.AddDate(0, 0, 7)); len(got) != 1 || got[0] != "Task1.ics" {
		t.Errorf("dated task in range: got %v, want [Task1.ics]", got)
	}
	if got := query("project-Proj1", oct.AddDate(-1, 0, 0), oct.AddDate(0, -6, 0)); len(got) != 0 {
		t.Errorf("dated task out of range: got %v, want none", got)
	}
	if got := query("project-Proj1", oct, time.Time{}); len(got) != 1 {
		t.Errorf("open-ended range: got %v, want [Task1.ics]", got)
	}
	// No DTSTART, DUE, COMPLETED or CREATED: matches any range (§9.9).
	if got := query(thingsdav.InboxID, oct.AddDate(-1, 0, 0), oct.AddDate(0, -6, 0)); len(got) != 1 {
		t.Errorf("undated inbox task: got %v, want [Task2.ics]", got)
	}
}

func TestTodoInRange(t *testing.T) {
	day := func(d int) time.Time { return time.Date(2026, 10, d, 0, 0, 0, 0, time.UTC) }
	todo := func(props map[string]time.Time, dur time.Duration) *ical.Component {
		c := ical.NewComponent(ical.CompToDo)
		for k, v := range props {
			c.Props.SetDateTime(k, v)
		}
		if dur > 0 {
			p := ical.NewProp(ical.PropDuration)
			p.SetDuration(dur)
			c.Props.Set(p)
		}
		return c
	}
	cases := []struct {
		name  string
		todo  *ical.Component
		r     davTimeRange
		match bool
	}{
		{"due inside", todo(map[string]time.Time{ical.PropDue: day(5)}, 0), davTimeRange{day(1), day(10)}, true},
		{"due before", todo(map[string]time.Time{ical.PropDue: day(5)}, 0), davTimeRange{day(6), day(10)}, false},
		{"start only after", todo(map[string]time.Time{ical.PropDateTimeStart: day(12)}, 0), davTimeRange{day(1), day(10)}, false},
		{"start+due spanning", todo(map[string]time.Time{ical.PropDateTimeStart: day(1), ical.PropDue: day(20)}, 0), davTimeRange{day(5), day(10)}, true},
		{"start+duration", todo(map[string]time.Time{ical.PropDateTimeStart: day(9)}, 48*time.Hour), davTimeRange{day(10), day(12)}, true},
		{"completed inside", todo(map[string]time.Time{ical.PropCompleted: day(3)}, 0), davTimeRange{day(1), day(10)}, true},
		{"created after end", todo(map[string]time.Time{ical.PropCreated: day(15)}, 0), davTimeRange{day(1), day(10)}, false},
		{"no dates", todo(nil, 0), davTimeRange{day(1), day(10)}, true},
		{"unbounded end", todo(map[string]time.Time{ical.PropDue: day(25)}, 0), davTimeRange{day(1), time.Time{}}, true},
	}
	for _, tc := range cases {
		if got := todoInRange(tc.todo, tc.r, time.UTC); got != tc.match {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.match)
		}
	}
}
