package server

import (
	"bufio"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readEvents reads an SSE stream until the named event (or EOF) and returns
// everything read.
func readEvents(t *testing.T, url, until string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var b strings.Builder
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	stop := false
	for sc.Scan() {
		line := sc.Text()
		b.WriteString(line + "\n")
		if line == "event: "+until {
			stop = true
			continue
		}
		if stop && strings.HasPrefix(line, "data: ") {
			break
		}
	}
	return b.String()
}

// The SNMP community sent with a scan must not appear in anything the API
// hands out: scan JSON, the event stream, exports, comparisons and history.
func TestScanOutputsNeverLeakCommunity(t *testing.T) {
	const secret = "s3cret-community"
	_, u, st := startStore(t)
	noLeak := func(what, body string) {
		t.Helper()
		if strings.Contains(body, secret) {
			t.Errorf("%s leaks the community: %s", what, body)
		}
	}

	r, b := doj(t, "POST", u+"/api/scans", `{"cidr":"10.0.0.0/30","community":"`+secret+`","live":true,"liveIntervalSec":5}`, "application/json")
	if r.StatusCode != 202 {
		t.Fatalf("start live: %d %s", r.StatusCode, b)
	}
	live := idOf(t, b)
	stream := readEvents(t, u+"/api/scans/"+live+"/events", "live")
	if !strings.Contains(stream, "event: snapshot") || !strings.Contains(stream, `"community":"***"`) {
		t.Fatalf("live stream has no redacted snapshot: %s", stream)
	}
	noLeak("live event stream", stream)
	_, b = doj(t, "GET", u+"/api/scans/"+live, "", "")
	if !strings.Contains(b, `"community":"***"`) {
		t.Fatalf("GET scan not redacted: %s", b)
	}
	noLeak("GET /api/scans/{id}", b)
	for _, f := range []string{"json", "csv"} {
		r, b := doj(t, "GET", u+"/api/scans/"+live+"/export?format="+f, "", "")
		if r.StatusCode != 200 {
			t.Fatalf("export %s: %d %s", f, r.StatusCode, b)
		}
		noLeak("export "+f, b)
	}

	// A second scan replaces the live one; both are saved to history.
	r, b = doj(t, "POST", u+"/api/scans", `{"cidr":"10.0.0.0/30","community":"`+secret+`"}`, "application/json")
	if r.StatusCode != 202 {
		t.Fatalf("start: %d %s", r.StatusCode, b)
	}
	cur := idOf(t, b)
	waitState(t, u, live, "done")
	waitState(t, u, cur, "done")
	stream = readEvents(t, u+"/api/scans/"+cur+"/events", "done")
	if !strings.Contains(stream, "event: done") {
		t.Fatalf("no done event: %s", stream)
	}
	noLeak("done event", stream)

	for _, id := range []string{live, cur} {
		r, b := doj(t, "GET", u+"/api/history/"+id, "", "")
		if r.StatusCode != 200 || !strings.Contains(b, `"community":"***"`) {
			t.Fatalf("history %s: %d %s", id, r.StatusCode, b)
		}
		noLeak("GET /api/history/{id}", b)
	}
	for _, f := range []string{"json", "csv"} {
		r, b := doj(t, "GET", u+"/api/compare/export?base="+live+"&current="+cur+"&format="+f, "", "")
		if r.StatusCode != 200 {
			t.Fatalf("compare export %s: %d %s", f, r.StatusCode, b)
		}
		noLeak("compare export "+f, b)
	}
	files, _ := filepath.Glob(filepath.Join(st.Dir(), "history", "*.json"))
	if len(files) != 2 {
		t.Fatalf("history files = %v", files)
	}
	for _, f := range files {
		data, _ := os.ReadFile(f)
		noLeak("history file "+filepath.Base(f), string(data))
	}

	// A file saved by an older version still holds the community; reading it
	// through the API hides it.
	old := `{"id":"old1","schemaVersion":2,"startedAt":"2020-01-01T10:00:00Z","state":"done","request":{"cidr":"10.0.0.0/30","community":"` + secret + `"}}`
	if err := os.WriteFile(filepath.Join(st.Dir(), "history", "20200101T100000Z-old1.json"), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	_, b = doj(t, "GET", u+"/api/history/old1", "", "")
	if !strings.Contains(b, `"community":"***"`) {
		t.Fatalf("old history file not redacted: %s", b)
	}
	noLeak("old history file via API", b)
}
