import { useCallback, useEffect, useRef, useState } from "react";
import { api, ApiError } from "../api/client";
import type { PendingRow } from "../api/types";
import { DataTable, type Column } from "../components/DataTable";
import styles from "./Reception.module.css";

const POLL_MS = 5000;
// How long an already-consented row stays on the board before it drops off:
// long enough for reception to see the patient was handled, short enough that
// the queue stays a list of things to actually do.
const HIDE_CONSENTED_MS = 15000;

export function Reception() {
  const [rows, setRows] = useState<PendingRow[]>([]);
  const [error, setError] = useState("");
  const [sending, setSending] = useState<Record<string, boolean>>({});
  const [hidden, setHidden] = useState<Record<string, boolean>>({});
  const hideTimers = useRef<Record<string, ReturnType<typeof setTimeout>>>({});

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

  // Arm the hide timer ONCE per patient, on first sighting. The poll above hands
  // back a fresh `rows` array every 5s, so re-arming per render would reset a 15s
  // timer that then never fires — the row would stay forever.
  useEffect(() => {
    for (const r of rows) {
      if (r.consented && !hideTimers.current[r.hms_patient_id]) {
        hideTimers.current[r.hms_patient_id] = setTimeout(
          () => setHidden((h) => ({ ...h, [r.hms_patient_id]: true })),
          HIDE_CONSENTED_MS,
        );
      }
    }
  }, [rows]);

  useEffect(() => {
    const timers = hideTimers.current;
    return () => Object.values(timers).forEach(clearTimeout);
  }, []);

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
      render: (r) =>
        r.consented ? (
          <span className={styles.badge} data-status="CONSENTED">Already consented — no action</span>
        ) : (
          <span className={styles.badge} data-status={r.status}>{r.status === "CODE_SENT" ? "Code sent" : "Awaiting"}</span>
        ),
    },
    {
      key: "action",
      header: "",
      render: (r) => (
        <button
          className={styles.action}
          disabled={r.consented || !!sending[r.hms_patient_id]}
          onClick={() => send(r.hms_patient_id)}
        >
          {r.status === "CODE_SENT" ? "Resend" : "Send code"}
        </button>
      ),
    },
  ];

  const visible = rows.filter((r) => !hidden[r.hms_patient_id]);

  return (
    <div className={styles.wrap}>
      <h1>Consent queue</h1>
      {error && <p className={styles.error} role="alert">{error}</p>}
      <DataTable columns={columns} rows={visible} empty="No patients awaiting consent." />
    </div>
  );
}
