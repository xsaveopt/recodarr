import { useToast } from "primevue/usetoast";
import { useConfirm } from "primevue/useconfirm";

/**
 * Thin wrapper around PrimeVue's Toast + ConfirmDialog that gives every panel a
 * consistent UX without each one duplicating the boilerplate.
 */
export function useNotify() {
  const toast = useToast();
  const confirm = useConfirm();

  function success(detail: string, summary = "Done") {
    toast.add({ severity: "success", summary, detail, life: 3000 });
  }

  function error(detail: unknown, summary = "Something went wrong") {
    const msg = detail instanceof Error ? detail.message : String(detail);
    toast.add({ severity: "error", summary, detail: msg, life: 6000 });
  }

  function info(detail: string, summary = "Info") {
    toast.add({ severity: "info", summary, detail, life: 3000 });
  }

  /** Wrap an async op so failures surface as a toast and the caller doesn't have to try/catch. */
  async function tryRun<T>(fn: () => Promise<T>, errSummary?: string): Promise<T | undefined> {
    try {
      return await fn();
    } catch (e) {
      error(e, errSummary);
      return undefined;
    }
  }

  function confirmDelete(opts: { name: string; onAccept: () => void | Promise<void> }) {
    confirm.require({
      message: `Delete "${opts.name}"? This cannot be undone.`,
      header: "Delete confirmation",
      icon: "pi pi-exclamation-triangle",
      acceptLabel: "Delete",
      rejectLabel: "Cancel",
      acceptClass: "p-button-danger",
      accept: async () => { await opts.onAccept(); },
    });
  }

  return { success, error, info, tryRun, confirmDelete };
}
