import { useEffect, useState } from "react";
import {
  PieChart, Pie, Cell, BarChart, Bar, XAxis, YAxis, Tooltip, ResponsiveContainer, Legend,
} from "recharts";
import { api } from "../api/client";
import type { ConsentStats } from "../api/types";
import { StatTile } from "../components/StatTile";
import styles from "./Dashboard.module.css";

const ACTIVE = "#15803d";
const WITHDRAWN = "#b45309";

export function Dashboard() {
  const [windowDays, setWindowDays] = useState(30);
  const [stats, setStats] = useState<ConsentStats | null>(null);
  const [pending, setPending] = useState(0);
  const [error, setError] = useState("");

  useEffect(() => {
    let alive = true;
    setError("");
    Promise.all([api.getStats(windowDays), api.getEmergencyPending()])
      .then(([s, p]) => { if (alive) { setStats(s); setPending(p.total); } })
      .catch((e) => { if (alive) setError(e instanceof Error ? e.message : "failed to load"); });
    return () => { alive = false; };
  }, [windowDays]);

  if (error) return <p style={{ color: "var(--status-danger)" }}>{error}</p>;
  if (!stats) return <p>Loading…</p>;

  const donut = [
    { name: "Active", value: stats.consents.active },
    { name: "Withdrawn", value: stats.consents.withdrawn },
  ];

  return (
    <div>
      <div className={styles.toolbar}>
        <label htmlFor="win">Window</label>
        <select id="win" value={windowDays} onChange={(e) => setWindowDays(Number(e.target.value))}>
          <option value={7}>Last 7 days</option>
          <option value={30}>Last 30 days</option>
          <option value={90}>Last 90 days</option>
        </select>
      </div>

      <div className={styles.tiles}>
        <StatTile label="Active consents" value={stats.consents.active} tone="active" />
        <StatTile label="Withdrawn" value={stats.consents.withdrawn} tone="withdrawn" />
        <StatTile label="Emergency overrides" value={stats.emergency.overrides} />
        <StatTile label="Pending review" value={pending} tone={pending > 0 ? "danger" : "default"} />
      </div>

      <div className={styles.charts}>
        <div className={styles.card}>
          <h3>Active vs withdrawn</h3>
          <ResponsiveContainer width="100%" height={240}>
            <PieChart>
              <Pie data={donut} dataKey="value" nameKey="name" innerRadius={60} outerRadius={90}>
                <Cell fill={ACTIVE} /><Cell fill={WITHDRAWN} />
              </Pie>
              <Legend /><Tooltip />
            </PieChart>
          </ResponsiveContainer>
        </div>
        <div className={styles.card}>
          <h3>By purpose</h3>
          <ResponsiveContainer width="100%" height={240}>
            <BarChart data={stats.by_purpose}>
              <XAxis dataKey="purpose" fontSize={12} /><YAxis allowDecimals={false} fontSize={12} />
              <Tooltip /><Legend />
              <Bar dataKey="active" name="Active" fill={ACTIVE} />
              <Bar dataKey="withdrawn" name="Withdrawn" fill={WITHDRAWN} />
            </BarChart>
          </ResponsiveContainer>
        </div>
      </div>

      <div className={styles.activity}>
        <span>Captures <b>{stats.activity.captures}</b></span>
        <span>Withdrawals <b>{stats.activity.withdrawals}</b></span>
        <span>Renewals <b>{stats.activity.renewals}</b></span>
        <span>· last {stats.activity.window_days} days</span>
      </div>
    </div>
  );
}
