import { useState } from "react";
import { NOTICE_TEXT, PURPOSES } from "../data/notice";

export function Consent({ busy, error, name, onConfirm }: {
  busy: boolean;
  error: string;
  name: string;
  onConfirm: (purposes: string[]) => void;
}) {
  // Purposes start granted; the patient unchecks to decline.
  const [granted, setGranted] = useState<Record<string, boolean>>(
    Object.fromEntries(PURPOSES.map((p) => [p.key, true])),
  );
  const toggle = (key: string) => setGranted((g) => ({ ...g, [key]: !g[key] }));
  const chosen = PURPOSES.filter((p) => granted[p.key]).map((p) => p.key);

  return (
    <section className="card">
      {name && <p className="greeting">Welcome, {name}</p>}
      <h2>Consent notice</h2>
      <p className="notice">{NOTICE_TEXT}</p>
      <ul className="purposes">
        {PURPOSES.map((p) => (
          <li key={p.key}>
            <label>
              <input type="checkbox" checked={granted[p.key]} onChange={() => toggle(p.key)} disabled={busy} />
              <span><strong>{p.label}</strong> — {p.description}</span>
            </label>
          </li>
        ))}
      </ul>
      {error && <p className="error" role="alert">{error}</p>}
      <button className="primary" disabled={busy || chosen.length === 0} onClick={() => onConfirm(chosen)}>
        {busy ? "Saving…" : "Confirm"}
      </button>
    </section>
  );
}
