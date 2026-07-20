import { useEffect, useState } from "react";
import { resolveClaim, capture, ApiError, retryable } from "./api/kiosk";
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

  // Errors are mapped per step: only the code step may blame the code. Past
  // resolve the code is already proven good, so a capture failure must never
  // send the patient back to the front desk for a pointless resend.
  function codeError(e: unknown): string {
    return e instanceof ApiError
      ? "Code not recognized — please ask the front desk to resend."
      : "Something went wrong. Please try again.";
  }

  function consentError(e: unknown): string {
    // Same predicate the retry loop uses: if it was worth retrying
    // automatically, it is worth the patient pressing again. Hung and refused
    // are the same situation to them, so they must not get different advice.
    if (retryable(e)) return "Something went wrong. Please try again.";
    if (e instanceof ApiError) {
      // 409 = an active consent already exists for this patient (a re-visit).
      if (e.status === 409) return "You have already given consent — nothing more to do.";
      // The session outlived the patient's time on this screen. Only a fresh
      // code fixes it, so name the cause instead of the generic "ask for help".
      if (e.status === 403) return "Your code has expired — please ask the front desk to resend.";
    }
    // Anything else: stay generic, never surface an internal error on a kiosk.
    return "We could not save your consent. Please ask the front desk for help.";
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
      setError(codeError(e));
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
      setError(consentError(e));
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
