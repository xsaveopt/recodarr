import { onUnmounted, ref } from "vue";

export interface EncodeProgress {
  jobId: number;
  title: string;
  percent: number;
  fps: number;
  eta: string;
}

/**
 * Subscribes to /api/worker/progress (SSE) for live encode progress across N
 * concurrent encodes. Holds one entry per in-flight job, keyed by jobId.
 *
 * Reconnects with exponential backoff (2s, 4s, 8s, …) capped at 30s, so a long
 * backend outage doesn't translate into a request storm. Successful open resets
 * the delay.
 *
 * Pruning: the SSE stream emits a final event with percent=0 when an encode
 * finishes (the worker fires it from the goroutine's defer), which removes the
 * job from the map. Callers that poll /api/worker/status can also call prune()
 * with the authoritative active set to clean up if SSE missed an event.
 */
export function useEncodeProgress(opts?: { onComplete?: (jobId: number) => void }) {
  const progressByJob = ref<Record<number, EncodeProgress>>({});
  const connected = ref(false);

  const minDelay = 2000;
  const maxDelay = 30000;
  let nextDelay = minDelay;

  let es: EventSource | null = null;
  let reconnectTimer: number | null = null;
  let stopped = false;

  function applyEvent(ev: EncodeProgress) {
    if (!ev.jobId) return;
    // Worker's defer fires a final {jobId, title, 0, 0, ""} when an encode
    // exits — remove the job from the map. A genuine "just started" event also
    // has percent=0 but typically arrives within a tick of follow-up progress,
    // so worst case we briefly drop and re-add a row. Acceptable.
    if (ev.percent === 0 && ev.fps === 0 && !ev.eta) {
      const had = progressByJob.value[ev.jobId] != null;
      const next = { ...progressByJob.value };
      delete next[ev.jobId];
      progressByJob.value = next;
      // Only fire onComplete if we actually had a tracked encode for this job;
      // ignore the spurious zero events that arrive before the first progress
      // tick on a brand-new encode.
      if (had) opts?.onComplete?.(ev.jobId);
      return;
    }
    progressByJob.value = { ...progressByJob.value, [ev.jobId]: ev };
  }

  function prune(activeIds: number[]) {
    const active = new Set(activeIds);
    let changed = false;
    const next: Record<number, EncodeProgress> = {};
    for (const [k, v] of Object.entries(progressByJob.value)) {
      if (active.has(Number(k))) next[Number(k)] = v;
      else changed = true;
    }
    if (changed) progressByJob.value = next;
  }

  function scheduleReconnect() {
    if (stopped) return;
    reconnectTimer = window.setTimeout(connect, nextDelay);
    nextDelay = Math.min(nextDelay * 2, maxDelay);
  }

  function connect() {
    if (stopped) return;
    es = new EventSource("/api/worker/progress");
    es.addEventListener("open", () => {
      connected.value = true;
      nextDelay = minDelay; // reset backoff on successful connect
    });
    es.addEventListener("progress", (ev) => {
      try {
        applyEvent(JSON.parse((ev as MessageEvent).data));
      } catch {
        /* ignore malformed */
      }
    });
    es.addEventListener("idle", () => {
      progressByJob.value = {};
    });
    es.addEventListener("error", () => {
      connected.value = false;
      es?.close();
      es = null;
      scheduleReconnect();
    });
  }

  connect();

  onUnmounted(() => {
    stopped = true;
    if (reconnectTimer != null) window.clearTimeout(reconnectTimer);
    es?.close();
    es = null;
  });

  return { progressByJob, connected, prune };
}
