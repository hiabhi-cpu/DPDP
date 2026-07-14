import { useCallback, useEffect, useState } from "react";
import { api, ApiError } from "../api/client";
import type { PendingRow } from "../api/types";
import { DataTable, type Column } from "../components/DataTable";
import styles from "./Reception.module.css";

const POLL_MS = 5000;

export function Reception() {
  const [rows, setRows] = useState<PendingRow[]>([]);
  const [error, setError] = useState("");
  const [sending, setSending] = useState<Record<string, boolean>>({});

  const load = useCallback(async () => {
    try {
      const all = await api.receptionRegistrations();
      // Completion by disappearance: DONE rows leave the queue.
      setRows(all.filter((r) => r.status !== "DONE"));
      setError("");
    } catch {
      setError("Could not load the queue.");
    }
  }, []);

  useEffect(() => {
    load();
    const t = setInterval(load, POLL_MS);
    return () => clearInterval(t);
  }, [load]);

  async function send(hms: string) {
    setSending((s) => ({ ...s, [hms]: true }));
    try {
      await api.sendCode(hms);
      await load();
    } catch (e) {
      setError(e instanceof ApiError && e.status === 429 ? "Please wait before resending." : "Could not send the code.");
    } finally {
      setSending((s) => ({ ...s, [hms]: false }));
    }
  }

  const columns: Column<PendingRow>[] = [
    { key: "name", header: "Patient", render: (r) => r.name },
    { key: "mobile", header: "Mobile", render: (r) => r.mobile },
    {
      key: "status",
      header: "Status",
      render: (r) => <span className={styles.badge} data-status={r.status}>{r.status === "CODE_SENT" ? "Code sent" : "Awaiting"}</span>,
    },
    {
      key: "action",
      header: "",
      render: (r) => (
        <button className={styles.action} disabled={!!sending[r.hms_patient_id]} onClick={() => send(r.hms_patient_id)}>
          {r.status === "CODE_SENT" ? "Resend" : "Send code"}
        </button>
      ),
    },
  ];

  return (
    <div className={styles.wrap}>
      <h1>Consent queue</h1>
      {error && <p className={styles.error} role="alert">{error}</p>}
      <DataTable columns={columns} rows={rows} empty="No patients awaiting consent." />
    </div>
  );
}
