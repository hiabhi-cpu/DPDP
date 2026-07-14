import { NavLink, Outlet, useNavigate } from "react-router-dom";
import { useAuth } from "../auth/AuthContext";
import styles from "./AppShell.module.css";

export function AppShell() {
  const { user, logout } = useAuth();
  const navigate = useNavigate();
  const onLogout = async () => { await logout(); navigate("/login", { replace: true }); };
  const cls = ({ isActive }: { isActive: boolean }) => (isActive ? `${styles.link} ${styles.active}` : styles.link);

  return (
    <div>
      <header className={styles.bar}>
        <span className={styles.brand}>Consent Manager</span>
        <nav className={styles.nav}>
          {user?.role === "reception" ? (
            <NavLink to="/reception" className={cls}>Consent queue</NavLink>
          ) : (
            <>
              <NavLink to="/" end className={cls}>Dashboard</NavLink>
              <NavLink to="/audit" className={cls}>Audit</NavLink>
              <NavLink to="/emergency" className={cls}>Emergency</NavLink>
            </>
          )}
        </nav>
        <span className={styles.spacer} />
        <span className={styles.user}>{user?.email}</span>
        <button className={styles.logout} onClick={onLogout}>Log out</button>
      </header>
      <main className={styles.main}><Outlet /></main>
    </div>
  );
}
