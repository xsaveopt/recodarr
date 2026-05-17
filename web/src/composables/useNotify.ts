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

  /**
   * Run an action that doesn't have a meaningful return value (e.g. settings save).
   * Shows a success toast on resolve, error toast on reject. Returns true/false so
   * callers can branch. Use this instead of `tryRun` for `void`-returning APIs —
   * `tryRun`'s return value can't distinguish "succeeded with no result" from
   * "failed" when T is void.
   */
  async function tryAct(
    fn: () => Promise<unknown>,
    successMsg: string,
    errSummary?: string,
  ): Promise<boolean> {
    try {
      await fn();
      success(successMsg);
      return true;
    } catch (e) {
      error(e, errSummary);
      return false;
    }
  }

  /**
   * Generic destructive-confirm. Pass `message` to override the default
   * "Delete X? This cannot be undone." line — useful when the destructive
   * action has nuance worth spelling out (e.g. "this only removes the DB row,
   * the file on disk is untouched"). `header` / `acceptLabel` are similarly
   * overridable; defaults match the simple-delete case.
   */
  function confirmDelete(opts: {
    name: string;
    message?: string;
    header?: string;
    acceptLabel?: string;
    onAccept: () => void | Promise<void>;
  }) {
    confirm.require({
      message: opts.message ?? `Delete "${opts.name}"? This cannot be undone.`,
      header: opts.header ?? "Delete confirmation",
      icon: "pi pi-exclamation-triangle",
      acceptLabel: opts.acceptLabel ?? "Delete",
      rejectLabel: "Cancel",
      acceptClass: "p-button-danger",
      accept: async () => {
        await opts.onAccept();
      },
    });
  }

  return { success, error, info, tryRun, tryAct, confirmDelete };
}
