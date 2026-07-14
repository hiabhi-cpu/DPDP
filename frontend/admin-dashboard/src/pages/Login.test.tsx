import { describe, it, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { Login } from "./Login";

const loginMock = vi.fn();
vi.mock("../auth/AuthContext", () => ({
  useAuth: () => ({ user: null, loading: false, login: loginMock, logout: vi.fn() }),
}));

function renderLogin() {
  return render(
    <MemoryRouter>
      <Login />
    </MemoryRouter>,
  );
}

describe("Login", () => {
  it("submits entered credentials", async () => {
    loginMock.mockResolvedValueOnce({ email: "admin@x.local", role: "admin" });
    renderLogin();
    await userEvent.type(screen.getByLabelText(/email/i), "admin@x.local");
    await userEvent.type(screen.getByLabelText(/password/i), "pw");
    await userEvent.click(screen.getByRole("button", { name: /sign in/i }));
    expect(loginMock).toHaveBeenCalledWith("admin@x.local", "pw");
  });

  it("shows an error when login fails", async () => {
    loginMock.mockRejectedValueOnce(new Error("invalid email or password"));
    renderLogin();
    await userEvent.type(screen.getByLabelText(/email/i), "admin@x.local");
    await userEvent.type(screen.getByLabelText(/password/i), "bad");
    await userEvent.click(screen.getByRole("button", { name: /sign in/i }));
    expect(await screen.findByText(/invalid email or password/i)).toBeInTheDocument();
  });
});
