import { useCallback, useEffect, useState } from "react";
import { api } from "../api/client";
import type { ReviewItem } from "../api/types";
import { DataTable, Column } from "../components/DataTable";
import { Modal } from "../components/Modal";
import styles from "./Emergency.module.css";

export function Emergency() {
  const [rows, setRows] = useState<ReviewItem[]>([]);
  const [selected, setSelected] = useState<ReviewItem | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const load = useCallback(() => {
    setError("");
    api.getEmergencyPending()
      .then((d) => setRows(d.pending))
      .catch((e) => setError(e instanceof Error ? e.message : "failed to load"));
  }, []);

  useEffect(() => { load(); }, [load]);

  const submit = async (decision: "VERIFIED" | "FLAGGED") => {
    if (!selected) return;
    setBusy(true);
    try {
      await api.reviewEmergency(selected.access_id, decision);
      setSelected(null);
      load();
    } catch (e) {
      setError(e instanceof Error ? e.message : "review failed");
    } finally {
      setBusy(false);
    }
  };

  const columns: Column<ReviewItem>[] = [
    { key: "doctor", header: "Doctor", render: (r) => r.doctor_id },
    { key: "reason", header: "Reason", render: (r) => r.emergency_reason },
    { key: "note", header: "Note", render: (r) => <span className={styles.note}>{r.clinical_note}</span> },
    { key: "deadline", header: "Deadline", render: (r) => (
      <span className={`${styles.badge} ${r.overdue ? styles.overdue : styles.ontime}`}>
        {r.overdue ? "Overdue" : new Date(r.dpo_deadline).toLocaleString()}
      </span>
    ) },
    { key: "action", header: "", render: (r) => (
      <button className={styles.review} onClick={() => setSelected(r)}>Review</button>
    ) },
  ];

  return (
    <div>
      <h2 style={{ marginTop: 0 }}>Emergency review queue</h2>
      {error && <p style={{ color: "var(--status-danger)" }}>{error}</p>}
      <DataTable columns={columns} rows={rows} empty="No pending emergency reviews." />

      <Modal open={selected !== null} title="Record review decision" onClose={() => setSelected(null)}>
        {selected && (
          <div>
            <p className={styles.note}>
              <b>{selected.doctor_id}</b> · {selected.emergency_reason}<br />
              {selected.clinical_note}
            </p>
            <div className={styles.actions}>
              <button className={styles.flag} disabled={busy} onClick={() => submit("FLAGGED")}>Flag</button>
              <button className={styles.verify} disabled={busy} onClick={() => submit("VERIFIED")}>Mark verified</button>
            </div>
          </div>
        )}
      </Modal>
    </div>
  );
}
