import { onMounted, onUnmounted, ref } from "vue";

import { api } from "@/api/client";
import type { WorkerStatus } from "@/types/api";

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
