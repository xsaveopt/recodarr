package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xsaveopt/recodarr/internal/handbrake"
)

func runAgent(t *testing.T, localFS bool) *agentEnv {
	t.Helper()
	env := newAgentEnv(t, localFS)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		env.runner.Run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return env
}

func encodeCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func waitForEmptyStore(t *testing.T, st *Store) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for len(st.List()) > 0 {
		if time.Now().After(deadline) {
			t.Fatalf("the agent still holds %d jobs", len(st.List()))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestClientEncodeUploadsAndDownloads(t *testing.T) {
	stubHandBrake(t, encodeOK)
	env := runAgent(t, false)
	source := writeFile(t, "S01E01.mkv", []byte("original source bytes"))

	var progress []handbrake.Progress
	res, err := NewClient(env.srv.URL+"/", testToken).Encode(encodeCtx(t), source, handbrake.Settings{},
		func(p handbrake.Progress) { progress = append(progress, p) })
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if res.TempPath != handbrake.TempPath(source, "") {
		t.Fatalf("got temp path %q, want %q", res.TempPath, handbrake.TempPath(source, ""))
	}
	if got, _ := os.ReadFile(res.TempPath); string(got) != "encoded" {
		t.Fatalf("got %q at the temp path, want the downloaded output", got)
	}
	if res.FinalSize != int64(len("encoded")) {
		t.Fatalf("got final size %d", res.FinalSize)
	}
	if !strings.Contains(res.Log, "work result = 0") {
		t.Fatalf("got log %q, want the remote handbrake log", res.Log)
	}
	if got, _ := os.ReadFile(source); string(got) != "original source bytes" {
		t.Fatal("the client touched the source")
	}
	for _, p := range progress {
		if p.Percent != 50 {
			t.Fatalf("got unexpected progress %+v", p)
		}
	}
	waitForEmptyStore(t, env.store)
}

func TestClientEncodeInPlaceOnASharedFilesystem(t *testing.T) {
	stubHandBrake(t, encodeOK)
	env := runAgent(t, true)
	source := writeFile(t, "movie.mp4", []byte("shared bytes"))

	c := NewClient(env.srv.URL, testToken)
	c.SetLocalFS(true)
	res, err := c.Encode(encodeCtx(t), source, handbrake.Settings{}, nil)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if filepath.Dir(res.TempPath) != filepath.Dir(source) {
		t.Fatalf("got temp path %q, want it next to the source", res.TempPath)
	}
	if got, _ := os.ReadFile(res.TempPath); string(got) != "encoded" {
		t.Fatalf("got %q at the temp path", got)
	}
	if res.FinalSize != int64(len("encoded")) {
		t.Fatalf("got final size %d", res.FinalSize)
	}
	waitForEmptyStore(t, env.store)
}

func TestClientEncodeSurfacesARemoteFailureWithItsLog(t *testing.T) {
	stubHandBrake(t, encodeFail)
	env := runAgent(t, false)
	source := writeFile(t, "S01E02.mkv", []byte("src"))

	res, err := NewClient(env.srv.URL, testToken).Encode(encodeCtx(t), source, handbrake.Settings{}, nil)
	if err == nil || !strings.Contains(err.Error(), "agent encode") {
		t.Fatalf("got %v, want a remote encode failure", err)
	}
	if !strings.Contains(res.Log, "boom") {
		t.Fatalf("got log %q, want the remote stderr", res.Log)
	}
	if _, statErr := os.Stat(handbrake.TempPath(source, "")); !os.IsNotExist(statErr) {
		t.Fatal("a failed encode left a temp file next to the source")
	}
	waitForEmptyStore(t, env.store)
}

func TestClientEncodeRejectsAMissingSource(t *testing.T) {
	c := NewClient("http://127.0.0.1:1", testToken)
	_, err := c.Encode(context.Background(), filepath.Join(t.TempDir(), "gone.mkv"), handbrake.Settings{}, nil)
	if err == nil || !strings.Contains(err.Error(), "stat source") {
		t.Fatalf("got %v, want a stat failure", err)
	}
}

func TestClientEncodeReportsACreateError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(ErrorResponse{Error: "no room"})
	}))
	t.Cleanup(srv.Close)
	source := writeFile(t, "a.mkv", []byte("x"))

	_, err := NewClient(srv.URL, testToken).Encode(context.Background(), source, handbrake.Settings{}, nil)
	if err == nil || !strings.Contains(err.Error(), "no room") {
		t.Fatalf("got %v, want the agent's error message", err)
	}
}

func TestClientPing(t *testing.T) {
	env := newAgentEnv(t, true)
	hs, err := NewClient(env.srv.URL, testToken).Ping(context.Background())
	if err != nil {
		t.Fatalf("ping: %v", err)
	}
	if hs.SlotsMax != 1 || !hs.LocalFS {
		t.Fatalf("got %+v", hs)
	}

	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(down.Close)
	if _, err := NewClient(down.URL, testToken).Ping(context.Background()); err == nil {
		t.Fatal("a 503 healthz was reported as healthy")
	}

	garbage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	t.Cleanup(garbage.Close)
	if _, err := NewClient(garbage.URL, testToken).Ping(context.Background()); err == nil {
		t.Fatal("an undecodable healthz was reported as healthy")
	}
}

func TestClientCheckAuth(t *testing.T) {
	env := newAgentEnv(t, false)
	if err := NewClient(env.srv.URL, testToken).CheckAuth(context.Background()); err != nil {
		t.Fatalf("good token: %v", err)
	}
	if err := NewClient(env.srv.URL, "wrong").CheckAuth(context.Background()); err == nil {
		t.Fatal("a wrong token was accepted")
	}
}

func TestClientDelete(t *testing.T) {
	env := newAgentEnv(t, false)
	js, _ := env.store.Create(sampleRequest(), false)
	c := NewClient(env.srv.URL, testToken)
	if err := c.Delete(context.Background(), js.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok := env.store.Get(js.ID); ok {
		t.Fatal("the job survived a client delete")
	}
	if err := NewClient(env.srv.URL, "wrong").Delete(context.Background(), js.ID); err == nil {
		t.Fatal("an unauthorized delete was reported as success")
	}
}

func TestWatchUntilDoneFallsBackToPolling(t *testing.T) {
	for _, tc := range []struct {
		state   State
		wantErr bool
	}{
		{StateDone, false},
		{StateFailed, true},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/events") {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte("event: progress\ndata: {\"percent\":30}\n\n"))
				return
			}
			_ = json.NewEncoder(w).Encode(JobStateSnapshot{ID: "j", State: tc.state, Error: "x"})
		}))
		var seen []handbrake.Progress
		err := NewClient(srv.URL, testToken).watchUntilDone(context.Background(), "j",
			func(p handbrake.Progress) { seen = append(seen, p) })
		srv.Close()
		if (err != nil) != tc.wantErr {
			t.Fatalf("%s: got %v, wantErr=%v", tc.state, err, tc.wantErr)
		}
		if len(seen) != 1 || seen[0].Percent != 30 {
			t.Fatalf("%s: got progress %+v, want the streamed event", tc.state, seen)
		}
	}
}

func TestWatchUntilDoneUsesTheStateAsAFallbackMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("event: state\ndata: {\"state\":\"cancelled\"}\n\n"))
	}))
	t.Cleanup(srv.Close)
	err := NewClient(srv.URL, testToken).watchUntilDone(context.Background(), "j", nil)
	if err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("got %v, want the cancelled state named", err)
	}
}

func TestReadErrPrefersTheJSONMessage(t *testing.T) {
	if got := readErr(strings.NewReader(`{"error":"bad thing"}`)); got != "bad thing" {
		t.Fatalf("got %q", got)
	}
	if got := readErr(strings.NewReader("  plain text \n")); got != "plain text" {
		t.Fatalf("got %q", got)
	}
}

func TestContainerFor(t *testing.T) {
	for _, tc := range []struct {
		path, format, want string
	}{
		{"/m/a.mkv", "", "mkv"},
		{"/m/a.mp4", "", "mp4"},
		{"/m/a.M4V", "", "mp4"},
		{"/m/a.avi", "", "mkv"},
		{"/m/a.mkv", "mp4", "mp4"},
		{"/m/a.mp4", "mkv", "mkv"},
	} {
		if got := containerFor(tc.path, handbrake.Settings{ContainerFormat: tc.format}); got != tc.want {
			t.Fatalf("containerFor(%q, %q) = %q, want %q", tc.path, tc.format, got, tc.want)
		}
	}
}

func TestNewRequestSetsTheBearerOnlyWhenPresent(t *testing.T) {
	req, err := NewClient("http://agent/", "tok").newRequest(context.Background(), http.MethodGet, "/jobs", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if req.URL.String() != "http://agent"+PathPrefix+"/jobs" {
		t.Fatalf("got url %s", req.URL)
	}
	if req.Header.Get("Authorization") != "Bearer tok" {
		t.Fatalf("got auth %q", req.Header.Get("Authorization"))
	}
	anon, _ := NewClient("http://agent", "").newRequest(context.Background(), http.MethodGet, "/x", nil)
	if anon.Header.Get("Authorization") != "" {
		t.Fatal("an empty token still produced an Authorization header")
	}
}
