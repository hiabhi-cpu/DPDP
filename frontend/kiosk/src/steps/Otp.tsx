import { useState } from "react";

export function Otp({ busy, error, onSubmit }: {
  busy: boolean;
  error: string;
  onSubmit: (otp: string) => void;
}) {
  const [otp, setOtp] = useState("");
  return (
    <section className="card">
      <h2>Enter the OTP</h2>
      <label htmlFor="otp">OTP</label>
      <input
        id="otp"
        inputMode="numeric"
        autoComplete="one-time-code"
        maxLength={6}
        value={otp}
        onChange={(e) => setOtp(e.target.value.replace(/\D/g, ""))}
      />
      {error && <p className="error" role="alert">{error}</p>}
      <button className="primary" disabled={busy || otp.length !== 6} onClick={() => onSubmit(otp)}>
        Verify
      </button>
    </section>
  );
}
