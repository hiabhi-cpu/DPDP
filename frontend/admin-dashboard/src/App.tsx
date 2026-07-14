import { BrowserRouter, Routes, Route, Navigate } from "react-router-dom";
import { AuthProvider, useAuth } from "./auth/AuthContext";
import { ProtectedRoute } from "./components/ProtectedRoute";
import { AppShell } from "./components/AppShell";
import { Login } from "./pages/Login";
import { Dashboard } from "./pages/Dashboard";
import { Audit } from "./pages/Audit";
import { Emergency } from "./pages/Emergency";
import { Reception } from "./pages/Reception";
import { homePathForRole } from "./auth/roleHome";
import { type ReactNode } from "react";

function RequireRole({ roles, children }: { roles: string[]; children: ReactNode }) {
  const { user } = useAuth();
  if (user && !roles.includes(user.role)) return <Navigate to={homePathForRole(user.role)} replace />;
  return <>{children}</>;
}

export default function App() {
  return (
    <AuthProvider>
      <BrowserRouter>
        <Routes>
          <Route path="/login" element={<Login />} />
          <Route element={<ProtectedRoute><AppShell /></ProtectedRoute>}>
            <Route path="/" element={<RequireRole roles={["admin", "dpo"]}><Dashboard /></RequireRole>} />
            <Route path="/audit" element={<RequireRole roles={["admin", "dpo"]}><Audit /></RequireRole>} />
            <Route path="/emergency" element={<RequireRole roles={["admin", "dpo"]}><Emergency /></RequireRole>} />
            <Route path="/reception" element={<RequireRole roles={["reception"]}><Reception /></RequireRole>} />
          </Route>
        </Routes>
      </BrowserRouter>
    </AuthProvider>
  );
}
