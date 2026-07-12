import { useState } from "react";

export function Mobile({ busy, error, onSubmit }: {
  busy: boolean;
  error: string;
  onSubmit: (mobile: string) => void;
}) {
  const [mobile, setMobile] = useState("");
  return (
    <section className="card">
      <h2>Enter your mobile number</h2>
      <label htmlFor="mobile">Mobile number</label>
      <input
        id="mobile"
        inputMode="numeric"
        autoComplete="tel"
        maxLength={10}
        value={mobile}
        onChange={(e) => setMobile(e.target.value.replace(/\D/g, ""))}
      />
      {error && <p className="error" role="alert">{error}</p>}
      <button className="primary" disabled={busy || mobile.length !== 10} onClick={() => onSubmit(mobile)}>
        Send OTP
      </button>
    </section>
  );
}
