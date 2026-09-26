package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const encodeOK = `
out=""
while [ $# -gt 0 ]; do
  if [ "$1" = "-o" ]; then out="$2"; fi
  shift
done
printf 'encoded' > "$out"
echo "Encoding: task 1 of 1, 50.00 % (10.00 fps, avg 10.00 fps, ETA 00h00m10s)"
echo "work result = 0"
`

const encodeFail = `
echo "boom" >&2
exit 3
`

const encodeHang = `
out=""
while [ $# -gt 0 ]; do
  if [ "$1" = "-o" ]; then out="$2"; fi
  shift
done
printf 'partial' > "$out"
echo "Encoding: task 1 of 1, 10.00 % (1.00 fps, avg 1.00 fps, ETA 00h01m00s)"
exec sleep 30
`

func stubHandBrake(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	body := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo \"HandBrake 1.0.0-test\"; exit 0; fi\n" + script + "\n"
	if err := os.WriteFile(filepath.Join(dir, "HandBrakeCLI"), []byte(body), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func queueUploadedJob(t *testing.T, st *Store, content string) *JobStateSnapshot {
	t.Helper()
	req := sampleRequest()
	req.SizeBytes = int64(len(content))
	js, err := st.Create(req, false)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := os.WriteFile(st.SourcePath(js), []byte(content), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	if err := st.Update(js.ID, func(j *JobStateSnapshot) error {
		j.State = StateQueued
		return nil
	}); err != nil {
		t.Fatalf("queue: %v", err)
	}
	return js
}

func waitForState(t *testing.T, st *Store, id string, want State) *JobStateSnapshot {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if js, ok := st.Get(id); ok && js.State == want {
			return js
		}
		time.Sleep(10 * time.Millisecond)
	}
	js, _ := st.Get(id)
	t.Fatalf("job %s never reached %s, last seen %+v", id, want, js)
	return nil
}

func drain(ch <-chan Event) []Event {
	var out []Event
	timeout := time.After(10 * time.Second)
	for {
		select {
		case e, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, e)
		case <-timeout:
			return out
		}
	}
}

func TestNewRunnerClampsParallelism(t *testing.T) {
	r := NewRunner(openTestStore(t), 0, nil)
	if r.maxParallel != 1 {
		t.Fatalf("got %d, want at least one slot", r.maxParallel)
	}
}

func TestRunnerEncodesAQueuedJob(t *testing.T) {
	stubHandBrake(t, encodeOK)
	st := openTestStore(t)
	r := NewRunner(st, 1, nil)
	js := queueUploadedJob(t, st, "original source")

	ch, _ := r.Subscribe(js.ID)
	r.tick(context.Background())
	events := drain(ch)

	done := waitForState(t, st, js.ID, StateDone)
	if done.OutputSizeBytes != int64(len("encoded")) {
		t.Fatalf("got output size %d, want %d", done.OutputSizeBytes, len("encoded"))
	}
	if done.Progress.Percent != 50 || done.FinishedAt == nil {
		t.Fatalf("got %+v, want progress and a finish time recorded", done)
	}
	out, err := os.ReadFile(st.OutputPath(done))
	if err != nil || string(out) != "encoded" {
		t.Fatalf("got %q, %v at the output path", out, err)
	}
	src, _ := os.ReadFile(st.SourcePath(done))
	if string(src) != "original source" {
		t.Fatal("the uploaded source was overwritten")
	}
	logText, _ := os.ReadFile(st.LogPath(done))
	if !strings.Contains(string(logText), "work result = 0") {
		t.Fatalf("the handbrake log was not captured: %q", logText)
	}

	var sawProgress, sawDone bool
	for _, e := range events {
		if e.Progress != nil && e.Progress.Percent == 50 {
			sawProgress = true
		}
		if e.State != nil && e.State.State == StateDone {
			sawDone = true
		}
	}
	if !sawProgress || !sawDone {
		t.Fatalf("got events %+v, want progress and a done state", events)
	}
}

func TestRunnerEncodesALocalSourceInPlace(t *testing.T) {
	stubHandBrake(t, encodeOK)
	st := openTestStore(t)
	r := NewRunner(st, 1, nil)

	source := writeFile(t, "shared.mkv", []byte("shared source"))
	req := sampleRequest()
	req.SourcePath = source
	js, err := st.Create(req, true)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	r.tick(context.Background())
	done := waitForState(t, st, js.ID, StateDone)
	if done.LocalOutputPath == "" || filepath.Dir(done.LocalOutputPath) != filepath.Dir(source) {
		t.Fatalf("got local output %q, want a temp file next to %s", done.LocalOutputPath, source)
	}
	if got, _ := os.ReadFile(done.LocalOutputPath); string(got) != "encoded" {
		t.Fatalf("got %q in the local output", got)
	}
	if got, _ := os.ReadFile(source); string(got) != "shared source" {
		t.Fatal("the in-place encode committed over the shared source")
	}
	if _, err := os.Stat(st.OutputPath(done)); !os.IsNotExist(err) {
		t.Fatal("an in-place encode also wrote into the job dir")
	}
}

func TestRunnerMarksAFailedEncode(t *testing.T) {
	stubHandBrake(t, encodeFail)
	st := openTestStore(t)
	r := NewRunner(st, 1, nil)
	js := queueUploadedJob(t, st, "src")

	r.tick(context.Background())
	failed := waitForState(t, st, js.ID, StateFailed)
	if !strings.Contains(failed.Error, "HandBrakeCLI") {
		t.Fatalf("got error %q, want the handbrake failure", failed.Error)
	}
	logText, _ := os.ReadFile(st.LogPath(failed))
	if !strings.Contains(string(logText), "boom") {
		t.Fatalf("stderr was not captured in the log: %q", logText)
	}
}

func TestRunnerCancelStopsARunningEncode(t *testing.T) {
	stubHandBrake(t, encodeHang)
	st := openTestStore(t)
	r := NewRunner(st, 1, nil)
	js := queueUploadedJob(t, st, "src")

	if r.Cancel(js.ID) {
		t.Fatal("cancelled a job that was not running")
	}

	r.tick(context.Background())
	waitForState(t, st, js.ID, StateEncoding)
	deadline := time.Now().Add(10 * time.Second)
	for !r.Cancel(js.ID) {
		if time.Now().After(deadline) {
			t.Fatal("the encode never became cancellable")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancelled := waitForState(t, st, js.ID, StateCancelled)
	if cancelled.Error != "cancelled" {
		t.Fatalf("got error %q, want cancelled", cancelled.Error)
	}
}

func TestRunnerTickRespectsTheSlotLimit(t *testing.T) {
	stubHandBrake(t, encodeHang)
	st := openTestStore(t)
	r := NewRunner(st, 1, nil)
	ctx, cancel := context.WithCancel(context.Background())
	first := queueUploadedJob(t, st, "a")
	second := queueUploadedJob(t, st, "b")

	r.tick(ctx)
	deadline := time.Now().Add(10 * time.Second)
	for {
		r.mu.Lock()
		n := len(r.active)
		r.mu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the first encode never started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	r.tick(ctx)

	queued := 0
	for _, js := range st.List() {
		if js.State == StateQueued {
			queued++
		}
	}
	if queued != 1 {
		t.Fatalf("got %d queued jobs, want one held back by the slot limit", queued)
	}

	cancel()
	for _, id := range []string{first.ID, second.ID} {
		js, _ := st.Get(id)
		if js.State == StateEncoding {
			waitForState(t, st, id, StateCancelled)
		}
	}
}

func TestSubscribeAndUnsubscribe(t *testing.T) {
	r := NewRunner(openTestStore(t), 1, nil)
	ch, unsub := r.Subscribe("job")
	r.publish("job", Event{Progress: &ProgressPayload{Percent: 5}})
	select {
	case e := <-ch:
		if e.Progress == nil || e.Progress.Percent != 5 {
			t.Fatalf("got %+v, want the published progress", e)
		}
	default:
		t.Fatal("the subscriber missed a published event")
	}

	unsub()
	unsub()
	r.mu.Lock()
	left := len(r.subs["job"]) + len(r.subIndex)
	r.mu.Unlock()
	if left != 0 {
		t.Fatal("unsubscribe left the channel registered")
	}
	r.publish("job", Event{Progress: &ProgressPayload{Percent: 6}})
	select {
	case e := <-ch:
		t.Fatalf("got %+v after unsubscribing", e)
	default:
	}
}

func TestCloseSubscribersClosesEveryChannel(t *testing.T) {
	r := NewRunner(openTestStore(t), 1, nil)
	a, _ := r.Subscribe("job")
	b, unsubB := r.Subscribe("job")
	r.closeSubscribers("job")
	for _, ch := range []<-chan Event{a, b} {
		if _, ok := <-ch; ok {
			t.Fatal("a subscriber channel is still open")
		}
	}
	unsubB()
}

func TestCleanupTerminalDropsOnlyExpiredTerminalJobs(t *testing.T) {
	st := openTestStore(t)
	r := NewRunner(st, 1, nil)
	old := time.Now().Add(-2 * terminalTTL)
	recent := time.Now()

	expired, _ := st.Create(sampleRequest(), true)
	fresh, _ := st.Create(sampleRequest(), true)
	unfinished, _ := st.Create(sampleRequest(), true)
	noTime, _ := st.Create(sampleRequest(), true)
	_ = st.Update(expired.ID, func(j *JobStateSnapshot) error {
		j.State, j.FinishedAt = StateDone, &old
		return nil
	})
	_ = st.Update(fresh.ID, func(j *JobStateSnapshot) error {
		j.State, j.FinishedAt = StateFailed, &recent
		return nil
	})
	_ = st.Update(unfinished.ID, func(j *JobStateSnapshot) error {
		j.FinishedAt = &old
		return nil
	})
	_ = st.Update(noTime.ID, func(j *JobStateSnapshot) error {
		j.State = StateCancelled
		return nil
	})

	r.cleanupTerminal()

	if _, ok := st.Get(expired.ID); ok {
		t.Fatal("an expired terminal job survived cleanup")
	}
	for _, id := range []string{fresh.ID, unfinished.ID, noTime.ID} {
		if _, ok := st.Get(id); !ok {
			t.Fatalf("job %s was cleaned up too early", id)
		}
	}
}

func TestRunStopsWhenTheContextIsDone(t *testing.T) {
	r := NewRunner(openTestStore(t), 1, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		r.Run(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
}
