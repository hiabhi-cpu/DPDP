import { useEffect, useState } from "react";
import { api } from "../api/client";
import type { AuditEvent, AuditLogPage } from "../api/types";
import { DataTable, type Column } from "../components/DataTable";
import styles from "./Audit.module.css";

const LIMIT = 25;

function maskKey(k: string): string {
  if (!k) return "—";
  return k.length > 12 ? `${k.slice(0, 8)}…${k.slice(-4)}` : k;
}

const columns: Column<AuditEvent>[] = [
  { key: "time", header: "Time", render: (e) => new Date(e.created_at).toLocaleString() },
  { key: "type", header: "Event", render: (e) => e.event_type },
  { key: "actor", header: "Actor", render: (e) => `${e.actor_type}:${e.actor_id}` },
  { key: "patient", header: "Patient", render: (e) => <span className={styles.mask}>{maskKey(e.patient_key)}</span> },
  { key: "ip", header: "IP", render: (e) => e.ip_address || "—" },
  { key: "details", header: "Details", render: (e) => <span className={styles.details}>{JSON.stringify(e.details)}</span> },
];

export function Audit() {
  const [page, setPage] = useState(1);
  const [eventType, setEventType] = useState("");
  const [filterInput, setFilterInput] = useState("");
  const [data, setData] = useState<AuditLogPage | null>(null);
  const [error, setError] = useState("");

  useEffect(() => {
    let alive = true;
    setError("");
    api.getAuditLogs({ page, limit: LIMIT, event_type: eventType || undefined })
      .then((d) => { if (alive) setData(d); })
      .catch((e) => { if (alive) setError(e instanceof Error ? e.message : "failed to load"); });
    return () => { alive = false; };
  }, [page, eventType]);

  const totalPages = data ? Math.max(1, Math.ceil(data.total / LIMIT)) : 1;

  return (
    <div>
      <div className={styles.toolbar}>
        <input placeholder="Filter by event type (e.g. CONSENT_GRANTED)"
          value={filterInput} onChange={(e) => setFilterInput(e.target.value)} style={{ width: 320 }} />
        <button onClick={() => { setPage(1); setEventType(filterInput.trim()); }}>Apply</button>
        {eventType && <button onClick={() => { setFilterInput(""); setEventType(""); setPage(1); }}>Clear</button>}
      </div>

      {error && <p style={{ color: "var(--status-danger)" }}>{error}</p>}
      <DataTable columns={columns} rows={data?.events ?? []} empty="No audit events." />

      <div className={styles.pager}>
        <button disabled={page <= 1} onClick={() => setPage((p) => p - 1)}>← Prev</button>
        <span>Page {page} / {totalPages}</span>
        <button disabled={page >= totalPages} onClick={() => setPage((p) => p + 1)}>Next →</button>
      </div>
    </div>
  );
}
