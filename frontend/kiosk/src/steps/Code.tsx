import { useState } from "react";

export function Code({ busy, error, onSubmit }: {
  busy: boolean;
  error: string;
  onSubmit: (otp: string) => void;
}) {
  const [otp, setOtp] = useState("");
  const valid = /^\d{6}$/.test(otp);
  return (
    <section className="card">
      <h1>Enter your code</h1>
      <p>Type the 6-digit code we just texted you.</p>
      <input
        className="code-input"
        inputMode="numeric"
        pattern="[0-9]*"
        maxLength={6}
        value={otp}
        onChange={(e) => setOtp(e.target.value.replace(/\D/g, "").slice(0, 6))}
        aria-label="6-digit code"
      />
      {error && <p className="error" role="alert">{error}</p>}
      <button className="primary" disabled={busy || !valid} onClick={() => onSubmit(otp)}>
        Continue
      </button>
    </section>
  );
}
