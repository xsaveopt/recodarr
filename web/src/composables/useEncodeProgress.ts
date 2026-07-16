import { onUnmounted, ref } from "vue";

export interface EncodeProgress {
  jobId: number;
  title: string;
  percent: number;
  fps: number;
  eta: string;
}

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
    if (ev.percent === 0 && ev.fps === 0 && !ev.eta) {
      const had = progressByJob.value[ev.jobId] != null;
      const next = { ...progressByJob.value };
      delete next[ev.jobId];
      progressByJob.value = next;
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
      nextDelay = minDelay;
    });
    es.addEventListener("progress", (ev) => {
      try {
        applyEvent(JSON.parse((ev as MessageEvent).data));
      } catch {}
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
