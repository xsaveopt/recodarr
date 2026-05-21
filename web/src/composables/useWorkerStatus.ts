import { onMounted, onUnmounted, ref } from "vue";

import { api } from "@/api/client";
import type { WorkerStatus } from "@/types/api";

// Singleton worker-status poller. Multiple components subscribe via
// useWorkerStatus(); polling starts when the first one mounts and stops when
// the last one unmounts. Anything that mutates worker state (pause/resume,
// window changes) should call refresh() so subscribers don't wait for the
// next tick.

const status = ref<WorkerStatus | null>(null);
let timer: number | null = null;
let refcount = 0;
let inFlight: Promise<void> | null = null;

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

function start() {
  if (timer != null) return;
  void refresh();
  timer = window.setInterval(() => void refresh(), 10000);
}

function stop() {
  if (timer != null) {
    window.clearInterval(timer);
    timer = null;
  }
}

export function useWorkerStatus() {
  onMounted(() => {
    refcount++;
    start();
  });
  onUnmounted(() => {
    refcount--;
    if (refcount <= 0) {
      refcount = 0;
      stop();
    }
  });

  return { status, refresh };
}
