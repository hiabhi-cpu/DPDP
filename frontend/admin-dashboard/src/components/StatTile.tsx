import styles from "./StatTile.module.css";

type Tone = "default" | "active" | "withdrawn" | "danger";

export function StatTile({ label, value, tone = "default" }: { label: string; value: number; tone?: Tone }) {
  return (
    <div className={styles.tile}>
      <span className={styles.label}>{label}</span>
      <span className={`${styles.value} ${styles[tone]}`}>{value}</span>
    </div>
  );
}
