import { onUnmounted, ref } from "vue";

export interface EncodeProgress {
  jobId: number;
  title: string;
  percent: number;
  fps: number;
  eta: string;
}

const EMPTY: EncodeProgress = { jobId: 0, title: "", percent: 0, fps: 0, eta: "" };

/**
 * Subscribes to /api/worker/progress (SSE) for live encode progress.
 * Reconnects with exponential backoff (2s, 4s, 8s, …) capped at 30s, so a long backend
 * outage doesn't translate into a request storm. Successful open resets the delay.
 */
export function useEncodeProgress() {
  const progress = ref<EncodeProgress>({ ...EMPTY });
  const connected = ref(false);

  const minDelay = 2000;
  const maxDelay = 30000;
  let nextDelay = minDelay;

  let es: EventSource | null = null;
  let reconnectTimer: number | null = null;
  let stopped = false;

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
        progress.value = JSON.parse((ev as MessageEvent).data);
      } catch { /* ignore malformed */ }
    });
    es.addEventListener("idle", () => { progress.value = { ...EMPTY }; });
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

  return { progress, connected };
}
