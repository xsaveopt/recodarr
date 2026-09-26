package agent

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

const testToken = "s3cret"

type agentEnv struct {
	store  *Store
	runner *Runner
	srv    *httptest.Server
}

func newAgentEnv(t *testing.T, localFS bool) *agentEnv {
	t.Helper()
	st := openTestStore(t)
	r := NewRunner(st, 1, nil)
	srv := httptest.NewServer(NewServer(st, r, testToken, localFS, nil).Handler())
	t.Cleanup(srv.Close)
	return &agentEnv{store: st, runner: r, srv: srv}
}

type agentResp struct {
	StatusCode int
	Header     http.Header
	Body       *bytes.Reader
}

func (e *agentEnv) do(t *testing.T, method, path string, body io.Reader, token string) *agentResp {
	t.Helper()
	req, err := http.NewRequest(method, e.srv.URL+PathPrefix+path, body)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	raw, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("%s %s: read body: %v", method, path, err)
	}
	return &agentResp{StatusCode: resp.StatusCode, Header: resp.Header, Body: bytes.NewReader(raw)}
}

func (e *agentEnv) postJob(t *testing.T, req JobRequest) *agentResp {
	t.Helper()
	raw, _ := json.Marshal(req)
	return e.do(t, http.MethodPost, "/jobs", bytes.NewReader(raw), testToken)
}

func wantCode(t *testing.T, resp *agentResp, want int) {
	t.Helper()
	if resp.StatusCode != want {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("got %d, want %d: %s", resp.StatusCode, want, body)
	}
}

func decode[T any](t *testing.T, resp *agentResp) T {
	t.Helper()
	var out T
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func TestHealthzIsOpenAndReportsSlots(t *testing.T) {
	env := newAgentEnv(t, true)
	if _, err := env.store.Create(sampleRequest(), true); err != nil {
		t.Fatalf("create: %v", err)
	}
	resp := env.do(t, http.MethodGet, "/healthz", nil, "")
	wantCode(t, resp, http.StatusOK)
	hs := decode[HealthSnapshot](t, resp)
	if hs.SlotsMax != 1 || hs.JobsActive != 1 || !hs.LocalFS {
		t.Fatalf("got %+v, want one slot, one active job and localFs", hs)
	}
	if !strings.HasPrefix(hs.Version, AgentVersion+"+") {
		t.Fatalf("got version %q", hs.Version)
	}
}

func TestProtectedRoutesNeedTheBearerToken(t *testing.T) {
	env := newAgentEnv(t, false)
	wantCode(t, env.do(t, http.MethodGet, "/jobs", nil, ""), http.StatusUnauthorized)
	wantCode(t, env.do(t, http.MethodGet, "/jobs", nil, "wrong"), http.StatusUnauthorized)
	wantCode(t, env.do(t, http.MethodGet, "/jobs", nil, testToken), http.StatusOK)
}

func TestCreateJobValidatesTheRequest(t *testing.T) {
	env := newAgentEnv(t, false)
	for name, req := range map[string]JobRequest{
		"zero size":     {Filename: "a.mkv", OutputContainer: "mkv"},
		"no filename":   {SizeBytes: 1, OutputContainer: "mkv"},
		"bad container": {Filename: "a.mkv", SizeBytes: 1, OutputContainer: "avi"},
	} {
		resp := env.postJob(t, req)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s: got %d, want 400", name, resp.StatusCode)
		}
		if er := decode[ErrorResponse](t, resp); er.Error == "" {
			t.Fatalf("%s: got no error message", name)
		}
	}
	wantCode(t, env.do(t, http.MethodPost, "/jobs", strings.NewReader("{"), testToken), http.StatusBadRequest)
}

func TestUploadFlowQueuesTheJob(t *testing.T) {
	env := newAgentEnv(t, false)
	content := "0123456789"
	req := sampleRequest()
	req.SizeBytes = int64(len(content))

	resp := env.postJob(t, req)
	wantCode(t, resp, http.StatusCreated)
	created := decode[JobCreateResponse](t, resp)
	if created.LocalSource || created.UploadURL != PathPrefix+"/jobs/"+created.JobID+"/source" {
		t.Fatalf("got %+v, want an upload url", created)
	}

	short := env.do(t, http.MethodPut, "/jobs/"+created.JobID+"/source", strings.NewReader("short"), testToken)
	wantCode(t, short, http.StatusBadRequest)

	ok := env.do(t, http.MethodPut, "/jobs/"+created.JobID+"/source", strings.NewReader(content), testToken)
	wantCode(t, ok, http.StatusAccepted)

	js, _ := env.store.Get(created.JobID)
	if js.State != StateQueued {
		t.Fatalf("got %s, want queued", js.State)
	}
	if got, _ := os.ReadFile(env.store.SourcePath(js)); string(got) != content {
		t.Fatalf("got %q on disk, want the upload", got)
	}

	again := env.do(t, http.MethodPut, "/jobs/"+created.JobID+"/source", strings.NewReader(content), testToken)
	wantCode(t, again, http.StatusConflict)
	wantCode(t, env.do(t, http.MethodPut, "/jobs/missing/source", strings.NewReader(content), testToken),
		http.StatusNotFound)
}

func TestCreateJobEncodesInPlaceOnlyWhenTheSourceMatches(t *testing.T) {
	content := []byte("shared library file")
	source := writeFile(t, "shared.mkv", content)
	hash, err := HashFile(source)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	base := JobRequest{
		Filename: "shared.mkv", SizeBytes: int64(len(content)), OutputContainer: "mkv",
		SourcePath: source, SourceHash: hash,
	}

	env := newAgentEnv(t, true)
	resp := env.postJob(t, base)
	wantCode(t, resp, http.StatusCreated)
	hit := decode[JobCreateResponse](t, resp)
	if !hit.LocalSource || hit.UploadURL != "" {
		t.Fatalf("got %+v, want an in-place job without an upload url", hit)
	}
	if js, _ := env.store.Get(hit.JobID); js.State != StateQueued {
		t.Fatalf("got %s, want the in-place job queued immediately", js.State)
	}

	misses := map[string]func(r *JobRequest){
		"hash mismatch": func(r *JobRequest) { r.SourceHash = "deadbeef" },
		"size mismatch": func(r *JobRequest) { r.SizeBytes++ },
		"relative path": func(r *JobRequest) { r.SourcePath = "shared.mkv" },
		"unclean path":  func(r *JobRequest) { r.SourcePath = source + "/../shared.mkv" },
		"missing file":  func(r *JobRequest) { r.SourcePath = source + ".gone" },
		"no hash":       func(r *JobRequest) { r.SourceHash = "" },
	}
	for name, mutate := range misses {
		req := base
		mutate(&req)
		resp := env.postJob(t, req)
		wantCode(t, resp, http.StatusCreated)
		if got := decode[JobCreateResponse](t, resp); got.LocalSource || got.UploadURL == "" {
			t.Fatalf("%s: got %+v, want a fallback to upload", name, got)
		}
	}

	remote := newAgentEnv(t, false)
	resp = remote.postJob(t, base)
	wantCode(t, resp, http.StatusCreated)
	if got := decode[JobCreateResponse](t, resp); got.LocalSource {
		t.Fatal("an agent without localFS accepted an in-place job")
	}
}

func TestGetAndListJobs(t *testing.T) {
	env := newAgentEnv(t, false)
	js, _ := env.store.Create(sampleRequest(), false)

	resp := env.do(t, http.MethodGet, "/jobs/"+js.ID, nil, testToken)
	wantCode(t, resp, http.StatusOK)
	got := decode[JobStateSnapshot](t, resp)
	if got.ID != js.ID || got.Request == nil || got.Request.Filename != "S01E01.mkv" {
		t.Fatalf("got %+v, want the full job", got)
	}

	list := decode[[]JobStateSnapshot](t, env.do(t, http.MethodGet, "/jobs", nil, testToken))
	if len(list) != 1 || list[0].Request != nil {
		t.Fatalf("got %+v, want one job with the request stripped", list)
	}

	wantCode(t, env.do(t, http.MethodGet, "/jobs/missing", nil, testToken), http.StatusNotFound)
}

func TestDownloadOutputOnlyWhenDone(t *testing.T) {
	env := newAgentEnv(t, false)
	js, _ := env.store.Create(sampleRequest(), false)

	wantCode(t, env.do(t, http.MethodGet, "/jobs/"+js.ID+"/output", nil, testToken), http.StatusConflict)
	wantCode(t, env.do(t, http.MethodGet, "/jobs/missing/output", nil, testToken), http.StatusNotFound)

	if err := os.WriteFile(env.store.OutputPath(js), []byte("encoded"), 0o644); err != nil {
		t.Fatalf("write output: %v", err)
	}
	_ = env.store.Update(js.ID, func(j *JobStateSnapshot) error {
		j.State = StateDone
		return nil
	})
	resp := env.do(t, http.MethodGet, "/jobs/"+js.ID+"/output", nil, testToken)
	wantCode(t, resp, http.StatusOK)
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "encoded" {
		t.Fatalf("got %q, want the output file", body)
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, `"S01E01.mkv"`) {
		t.Fatalf("got Content-Disposition %q", cd)
	}
}

func TestDownloadLog(t *testing.T) {
	env := newAgentEnv(t, false)
	js, _ := env.store.Create(sampleRequest(), false)
	if err := os.WriteFile(env.store.LogPath(js), []byte("hb log"), 0o644); err != nil {
		t.Fatalf("write log: %v", err)
	}
	resp := env.do(t, http.MethodGet, "/jobs/"+js.ID+"/log", nil, testToken)
	wantCode(t, resp, http.StatusOK)
	if body, _ := io.ReadAll(resp.Body); string(body) != "hb log" {
		t.Fatalf("got %q", body)
	}
	wantCode(t, env.do(t, http.MethodGet, "/jobs/missing/log", nil, testToken), http.StatusNotFound)
}

func TestDeleteJobIsIdempotent(t *testing.T) {
	env := newAgentEnv(t, false)
	js, _ := env.store.Create(sampleRequest(), false)
	wantCode(t, env.do(t, http.MethodDelete, "/jobs/"+js.ID, nil, testToken), http.StatusNoContent)
	if _, ok := env.store.Get(js.ID); ok {
		t.Fatal("the job survived a delete")
	}
	wantCode(t, env.do(t, http.MethodDelete, "/jobs/"+js.ID, nil, testToken), http.StatusNoContent)
}

func readSSE(t *testing.T, r io.Reader) []string {
	t.Helper()
	var events []string
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "event: ") || strings.HasPrefix(line, "data: ") {
			events = append(events, line)
		}
	}
	return events
}

func TestEventsForATerminalJobSendStateAndClose(t *testing.T) {
	env := newAgentEnv(t, false)
	js, _ := env.store.Create(sampleRequest(), false)
	_ = env.store.Update(js.ID, func(j *JobStateSnapshot) error {
		j.State = StateFailed
		j.Error = "nope"
		j.Progress = ProgressPayload{Percent: 42}
		return nil
	})

	resp := env.do(t, http.MethodGet, "/jobs/"+js.ID+"/events", nil, testToken)
	wantCode(t, resp, http.StatusOK)
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("got content type %q", ct)
	}
	got := strings.Join(readSSE(t, resp.Body), "\n")
	if !strings.Contains(got, "event: state") || !strings.Contains(got, `"state":"failed"`) ||
		!strings.Contains(got, `"error":"nope"`) {
		t.Fatalf("got %q, want the failed state", got)
	}
	if !strings.Contains(got, "event: progress") || !strings.Contains(got, `"percent":42`) {
		t.Fatalf("got %q, want the last progress replayed", got)
	}
	wantCode(t, env.do(t, http.MethodGet, "/jobs/missing/events", nil, testToken), http.StatusNotFound)
}

func TestEventsStreamLiveUpdatesUntilTerminal(t *testing.T) {
	env := newAgentEnv(t, false)
	js, _ := env.store.Create(sampleRequest(), false)

	go func() {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			env.runner.mu.Lock()
			n := len(env.runner.subs[js.ID])
			env.runner.mu.Unlock()
			if n > 0 {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		env.runner.publish(js.ID, Event{Progress: &ProgressPayload{Percent: 12.5}})
		env.runner.finish(js.ID, StateDone, nil, 7, "")
	}()

	resp := env.do(t, http.MethodGet, "/jobs/"+js.ID+"/events", nil, testToken)
	wantCode(t, resp, http.StatusOK)
	got := strings.Join(readSSE(t, resp.Body), "\n")
	if !strings.Contains(got, `"state":"awaiting_source"`) {
		t.Fatalf("got %q, want the initial state first", got)
	}
	if !strings.Contains(got, `"percent":12.5`) {
		t.Fatalf("got %q, want the live progress", got)
	}
	if !strings.Contains(got, `"state":"done"`) {
		t.Fatalf("got %q, want the terminal state", got)
	}
}

func TestFirstLine(t *testing.T) {
	if got := firstLine("HandBrake 1.9\nextra"); got != "HandBrake 1.9" {
		t.Fatalf("got %q", got)
	}
	if got := firstLine("single"); got != "single" {
		t.Fatalf("got %q", got)
	}
}
