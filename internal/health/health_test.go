package health

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xsaveopt/recodarr/internal/store"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "health.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func set(t *testing.T, st *store.Store, k, v string) {
	t.Helper()
	if err := st.SetSetting(context.Background(), k, v); err != nil {
		t.Fatalf("set %s: %v", k, err)
	}
}

func addMapping(t *testing.T, st *store.Store) {
	t.Helper()
	ctx := context.Background()
	pid, err := st.UpsertProfile(ctx, store.ProfileRow{Name: "health", Encoder: "x265"})
	if err != nil {
		t.Fatalf("profile: %v", err)
	}
	if _, err := st.CreateTagMapping(ctx, store.TagMappingRow{ArrKind: "sonarr", TagID: 1, ProfileID: pid}); err != nil {
		t.Fatalf("mapping: %v", err)
	}
}

func issueBySource(snap Snapshot, source string) (Issue, bool) {
	for _, iss := range snap.Issues {
		if iss.Source == source {
			return iss, true
		}
	}
	return Issue{}, false
}

func withoutHandbrake(snap Snapshot) []Issue {
	out := []Issue{}
	for _, iss := range snap.Issues {
		if iss.Source != "handbrake" {
			out = append(out, iss)
		}
	}
	return out
}

func TestProbeWarnsWithoutTagMappings(t *testing.T) {
	st := openStore(t)
	snap := New(st).probe(context.Background())
	iss, ok := issueBySource(snap, "config")
	if !ok || iss.Level != LevelWarn {
		t.Fatalf("got %+v, want a config warning", snap.Issues)
	}
	if snap.OK {
		t.Fatal("a snapshot with issues reported OK")
	}

	addMapping(t, st)
	if _, ok := issueBySource(New(st).probe(context.Background()), "config"); ok {
		t.Fatal("the config warning stayed after adding a mapping")
	}
}

func TestProbeFlagsUnreachableArrAndQbit(t *testing.T) {
	st := openStore(t)
	addMapping(t, st)
	ctx := context.Background()

	arrSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "good" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte("{}"))
	}))
	t.Cleanup(arrSrv.Close)
	qbitSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("password") == "good" {
			_, _ = w.Write([]byte("Ok."))
			return
		}
		_, _ = w.Write([]byte("Fails."))
	}))
	t.Cleanup(qbitSrv.Close)

	goodArr, _ := st.CreateArrInstance(ctx, store.ArrInstanceRow{Kind: "sonarr", Name: "ok", URL: arrSrv.URL, APIKey: "good", Enabled: true})
	badArr, _ := st.CreateArrInstance(ctx, store.ArrInstanceRow{Kind: "radarr", Name: "bad", URL: arrSrv.URL, APIKey: "nope", Enabled: true})
	_, _ = st.CreateArrInstance(ctx, store.ArrInstanceRow{Kind: "radarr", Name: "off", URL: arrSrv.URL, APIKey: "nope", Enabled: false})
	goodQbit, _ := st.UpsertQbitInstance(ctx, store.QbitInstanceRow{Name: "q1", URL: qbitSrv.URL, Password: "good"})
	badQbit, _ := st.UpsertQbitInstance(ctx, store.QbitInstanceRow{Name: "q2", URL: qbitSrv.URL, Password: "bad"})

	issues := withoutHandbrake(New(st).probe(ctx))
	if len(issues) != 2 {
		t.Fatalf("got %+v, want exactly the bad arr and the bad qbit", issues)
	}
	byTitle := map[string]Issue{}
	for _, iss := range issues {
		byTitle[iss.Source] = iss
		if iss.Level != LevelError {
			t.Fatalf("got %+v, want errors", iss)
		}
	}
	if iss, ok := byTitle["arr:"+strconv.FormatInt(badArr, 10)]; !ok || !strings.Contains(iss.Title, "bad (radarr)") {
		t.Fatalf("got %+v, want the bad arr flagged", issues)
	}
	if iss, ok := byTitle["qbit:"+strconv.FormatInt(badQbit, 10)]; !ok || !strings.Contains(iss.Title, "login failed") {
		t.Fatalf("got %+v, want the bad qbit flagged", issues)
	}
	if _, ok := byTitle["arr:"+strconv.FormatInt(goodArr, 10)]; ok {
		t.Fatal("the healthy arr was flagged")
	}
	if _, ok := byTitle["qbit:"+strconv.FormatInt(goodQbit, 10)]; ok {
		t.Fatal("the healthy qbit was flagged")
	}
}

func TestProbeChecksTheRemoteAgent(t *testing.T) {
	st := openStore(t)
	addMapping(t, st)
	set(t, st, "agent_enabled", "true")

	iss, ok := issueBySource(New(st).probe(context.Background()), "agent")
	if !ok || iss.Level != LevelWarn || !strings.Contains(iss.Title, "missing") {
		t.Fatalf("got %+v, want a missing-config warning", iss)
	}

	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	set(t, st, "agent_url", dead.URL)
	set(t, st, "agent_token", "tok")
	iss, ok = issueBySource(New(st).probe(context.Background()), "agent")
	if !ok || iss.Level != LevelError || !strings.Contains(iss.Title, "unreachable") {
		t.Fatalf("got %+v, want an unreachable error", iss)
	}

	alive := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"version":"1"}`))
	}))
	t.Cleanup(alive.Close)
	set(t, st, "agent_url", alive.URL)
	if iss, ok := issueBySource(New(st).probe(context.Background()), "agent"); ok {
		t.Fatalf("got %+v for a healthy agent", iss)
	}

	set(t, st, "agent_enabled", "false")
	set(t, st, "agent_url", dead.URL)
	if _, ok := issueBySource(New(st).probe(context.Background()), "agent"); ok {
		t.Fatal("a disabled agent was probed")
	}
}

func TestProbeWarnsAboutSeedWaitsWithoutQbit(t *testing.T) {
	st := openStore(t)
	addMapping(t, st)
	if _, ok := issueBySource(New(st).probe(context.Background()), "qbit"); ok {
		t.Fatal("warned about qbit with no waiting jobs")
	}
	if _, err := st.InsertJob(context.Background(), store.JobRow{
		ArrKind: "sonarr", Title: "t", FilePath: "/m/a.mkv", Status: "waiting_for_seed",
	}); err != nil {
		t.Fatalf("insert job: %v", err)
	}
	iss, ok := issueBySource(New(st).probe(context.Background()), "qbit")
	if !ok || iss.Level != LevelWarn || !strings.Contains(iss.Detail, "1 job(s)") {
		t.Fatalf("got %+v, want the stuck job counted", iss)
	}
}

func TestProbeSortsErrorsFirst(t *testing.T) {
	st := openStore(t)
	set(t, st, "agent_enabled", "true")
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	set(t, st, "agent_url", dead.URL)
	set(t, st, "agent_token", "tok")

	snap := New(st).probe(context.Background())
	seenWarn := false
	for _, iss := range snap.Issues {
		if iss.Level == LevelWarn {
			seenWarn = true
		} else if seenWarn {
			t.Fatalf("got %+v, want every error before the warnings", snap.Issues)
		}
	}
	if !seenWarn {
		t.Fatalf("got %+v, want at least the config warning", snap.Issues)
	}
}

func TestSnapshotIsCached(t *testing.T) {
	st := openStore(t)
	c := New(st)
	first := c.Snapshot(context.Background())
	addMapping(t, st)
	second := c.Snapshot(context.Background())
	if !second.CheckedAt.Equal(first.CheckedAt) {
		t.Fatal("a second snapshot inside the TTL re-probed")
	}
	if _, ok := issueBySource(second, "config"); !ok {
		t.Fatal("the cached snapshot lost its issues")
	}

	c.mu.Lock()
	c.lastT = time.Now().Add(-2 * cacheTTL)
	c.mu.Unlock()
	third := c.Snapshot(context.Background())
	if _, ok := issueBySource(third, "config"); ok {
		t.Fatal("an expired cache was served")
	}
}

type hook struct {
	mu   sync.Mutex
	msgs []map[string]any
}

func (h *hook) start(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		h.mu.Lock()
		h.msgs = append(h.msgs, body)
		h.mu.Unlock()
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func (h *hook) forSource(source string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, m := range h.msgs {
		if m["source"] == source {
			out = append(out, m["transition"].(string))
		}
	}
	return out
}

func TestTickNotifiesOnOpenAndResolve(t *testing.T) {
	st := openStore(t)
	h := &hook{}
	set(t, st, "notify_url", h.start(t))
	c := New(st)

	c.tick(context.Background())
	c.tick(context.Background())
	if got := h.forSource("config"); len(got) != 1 || got[0] != "opened" {
		t.Fatalf("got %v, want one opened notification", got)
	}

	addMapping(t, st)
	c.tick(context.Background())
	if got := h.forSource("config"); len(got) != 2 || got[1] != "resolved" {
		t.Fatalf("got %v, want the issue resolved", got)
	}

	snap := c.Snapshot(context.Background())
	if _, ok := issueBySource(snap, "config"); ok {
		t.Fatal("the snapshot after tick is stale")
	}
}

func TestRunStopsWithTheContext(t *testing.T) {
	c := New(openStore(t))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		c.Run(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
}
