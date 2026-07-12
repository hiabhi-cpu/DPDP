export function Welcome({ onStart }: { onStart: () => void }) {
  return (
    <section className="card">
      <h1>Consent</h1>
      <p>Please give your consent for how this hospital uses your data.</p>
      <button className="primary" onClick={onStart}>Start</button>
    </section>
  );
}
