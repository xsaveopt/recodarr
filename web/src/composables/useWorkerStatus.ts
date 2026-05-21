import { onMounted, onUnmounted, ref } from "vue";

import { api } from "@/api/client";
import type { WorkerStatus } from "@/types/api";

// Singleton worker-status poller. Multiple components subscribe via
// useWorkerStatus(); polling starts when the first one mounts and stops when
// the last one unmounts. Polling is also paused while the tab is hidden so
// background tabs don't churn the API.
//
// Anything that mutates worker state (pause/resume, window changes) should
// call refresh() so subscribers don't wait for the next tick.

const POLL_MS = 10000;

const status = ref<WorkerStatus | null>(null);
let timer: number | null = null;
let refcount = 0;
let inFlight: Promise<void> | null = null;
let visibilityHooked = false;

async function refresh(): Promise<void> {
  if (inFlight) return inFlight;
  inFlight = (async () => {
    try {
      status.value = await api.worker.status();
    } catch {
      // Background poll — stay quiet, the next tick will retry.
    } finally {
      inFlight = null;
    }
  })();
  return inFlight;
}

function startTimer() {
  if (timer != null) return;
  timer = window.setInterval(() => void refresh(), POLL_MS);
}

function stopTimer() {
  if (timer != null) {
    window.clearInterval(timer);
    timer = null;
  }
}

function onVisibilityChange() {
  if (refcount <= 0) return;
  if (document.hidden) {
    stopTimer();
  } else {
    void refresh();
    startTimer();
  }
}

export function useWorkerStatus() {
  onMounted(() => {
    refcount++;
    if (!visibilityHooked) {
      document.addEventListener("visibilitychange", onVisibilityChange);
      visibilityHooked = true;
    }
    void refresh();
    if (!document.hidden) startTimer();
  });
  onUnmounted(() => {
    refcount--;
    if (refcount <= 0) {
      refcount = 0;
      stopTimer();
    }
  });

  return { status, refresh };
}
