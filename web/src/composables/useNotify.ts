import { useToast } from "primevue/usetoast";
import { useConfirm } from "primevue/useconfirm";

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

  async function tryRun<T>(fn: () => Promise<T>, errSummary?: string): Promise<T | undefined> {
    try {
      return await fn();
    } catch (e) {
      error(e, errSummary);
      return undefined;
    }
  }

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
