import { useEffect, useState } from "react";
import { resolveClaim, capture, ApiError } from "./api/kiosk";
import { Code } from "./steps/Code";
import { Consent } from "./steps/Consent";
import { Done } from "./steps/Done";

type Step = "code" | "consent" | "done";

const RESET_MS = 5000;

export function App() {
  const [step, setStep] = useState<Step>("code");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [mobile, setMobile] = useState("");
  const [sessionId, setSessionId] = useState("");
  const [name, setName] = useState("");
  const [hmsPatientId, setHmsPatientId] = useState("");

  function reset() {
    setStep("code");
    setBusy(false);
    setError("");
    setMobile("");
    setSessionId("");
    setName("");
    setHmsPatientId("");
  }

  useEffect(() => {
    if (step !== "done") return;
    const t = setTimeout(reset, RESET_MS);
    return () => clearTimeout(t);
  }, [step]);

  function msg(e: unknown): string {
    return e instanceof ApiError
      ? "Code not recognized — please ask the front desk to resend."
      : "Something went wrong. Please try again.";
  }

  async function onCode(otp: string) {
    setBusy(true);
    setError("");
    try {
      const r = await resolveClaim(otp);
      setSessionId(r.session_id);
      setMobile(r.mobile);
      setName(r.name);
      setHmsPatientId(r.hms_patient_id);
      setStep("consent");
    } catch (e) {
      setError(msg(e));
    } finally {
      setBusy(false);
    }
  }

  async function onConfirm(purposes: string[]) {
    setBusy(true);
    setError("");
    try {
      await capture(mobile, sessionId, purposes, hmsPatientId);
      setStep("done");
    } catch (e) {
      setError(msg(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <main className="shell">
      {step === "code" && <Code busy={busy} error={error} onSubmit={onCode} />}
      {step === "consent" && <Consent busy={busy} error={error} name={name} onConfirm={onConfirm} />}
      {step === "done" && <Done />}
    </main>
  );
}
