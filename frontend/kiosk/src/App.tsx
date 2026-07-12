import { useEffect, useState } from "react";
import { sendOtp, verifyOtp, capture, ApiError } from "./api/kiosk";
import { Welcome } from "./steps/Welcome";
import { Mobile } from "./steps/Mobile";
import { Otp } from "./steps/Otp";
import { Consent } from "./steps/Consent";
import { Done } from "./steps/Done";

type Step = "welcome" | "mobile" | "otp" | "consent" | "done";

const RESET_MS = 5000;

export function App() {
  const [step, setStep] = useState<Step>("welcome");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [mobile, setMobile] = useState("");
  const [referenceId, setReferenceId] = useState("");
  const [sessionId, setSessionId] = useState("");

  function reset() {
    setStep("welcome");
    setBusy(false);
    setError("");
    setMobile("");
    setReferenceId("");
    setSessionId("");
  }

  // Kiosk hygiene: clear the screen a few seconds after completion so no
  // patient data lingers for the next person.
  useEffect(() => {
    if (step !== "done") return;
    const t = setTimeout(reset, RESET_MS);
    return () => clearTimeout(t);
  }, [step]);

  function msg(e: unknown): string {
    return e instanceof ApiError ? e.message : "Something went wrong. Please try again.";
  }

  async function onMobile(m: string) {
    setBusy(true);
    setError("");
    try {
      const { reference_id } = await sendOtp(m);
      setMobile(m);
      setReferenceId(reference_id);
      setStep("otp");
    } catch (e) {
      setError(msg(e));
    } finally {
      setBusy(false);
    }
  }

  async function onOtp(otp: string) {
    setBusy(true);
    setError("");
    try {
      const { session_id } = await verifyOtp(mobile, referenceId, otp);
      setSessionId(session_id);
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
      await capture(mobile, sessionId, purposes);
      setStep("done");
    } catch (e) {
      setError(msg(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <main className="shell">
      {step === "welcome" && <Welcome onStart={() => setStep("mobile")} />}
      {step === "mobile" && <Mobile busy={busy} error={error} onSubmit={onMobile} />}
      {step === "otp" && <Otp busy={busy} error={error} onSubmit={onOtp} />}
      {step === "consent" && <Consent busy={busy} error={error} onConfirm={onConfirm} />}
      {step === "done" && <Done />}
    </main>
  );
}
