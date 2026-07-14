import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { Reception } from "./Reception";

vi.mock("../api/client", () => ({
  api: { receptionRegistrations: vi.fn(), sendCode: vi.fn() },
  ApiError: class extends Error {},
}));
import { api } from "../api/client";

const rows = [
  { hms_patient_id: "PA-1", name: "Asha", mobile: "98****3210", status: "PENDING", registered_at: "2026-07-13T10:00:00Z" },
  { hms_patient_id: "PA-2", name: "Ravi", mobile: "97****0009", status: "CODE_SENT", registered_at: "2026-07-13T10:01:00Z" },
  { hms_patient_id: "PA-3", name: "Done Guy", mobile: "96****0000", status: "DONE", registered_at: "2026-07-13T10:02:00Z" },
];

describe("Reception queue", () => {
  beforeEach(() => vi.clearAllMocks());

  it("lists PENDING/CODE_SENT rows, hides DONE, masks mobile, Send vs Resend", async () => {
    (api.receptionRegistrations as any).mockResolvedValue(rows);
    render(<Reception />);
    await waitFor(() => expect(screen.getByText("Asha")).toBeInTheDocument());
    expect(screen.getByText("Ravi")).toBeInTheDocument();
    expect(screen.queryByText("Done Guy")).not.toBeInTheDocument(); // DONE hidden
    expect(screen.getByText("98****3210")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /send code/i })).toBeInTheDocument();  // PENDING
    expect(screen.getByRole("button", { name: /resend/i })).toBeInTheDocument();      // CODE_SENT
  });

  it("calls sendCode when the action is clicked", async () => {
    (api.receptionRegistrations as any).mockResolvedValue([rows[0]]);
    (api.sendCode as any).mockResolvedValue({ status: "sent" });
    render(<Reception />);
    await waitFor(() => expect(screen.getByText("Asha")).toBeInTheDocument());
    fireEvent.click(screen.getByRole("button", { name: /send code/i }));
    await waitFor(() => expect(api.sendCode).toHaveBeenCalledWith("PA-1"));
  });
});
