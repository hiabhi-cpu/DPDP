import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor, act } from "@testing-library/react";
import { Reception } from "./Reception";

vi.mock("../api/client", () => ({
  api: { receptionRegistrations: vi.fn(), sendCode: vi.fn() },
  ApiError: class extends Error {},
}));
import { api } from "../api/client";

const rows = [
  { hms_patient_id: "PA-1", name: "Asha", mobile: "98****3210", status: "PENDING", registered_at: "2026-07-13T10:00:00Z", consented: false },
  { hms_patient_id: "PA-2", name: "Ravi", mobile: "97****0009", status: "CODE_SENT", registered_at: "2026-07-13T10:01:00Z", consented: false },
  { hms_patient_id: "PA-3", name: "Done Guy", mobile: "96****0000", status: "DONE", registered_at: "2026-07-13T10:02:00Z", consented: false },
];

const consentedRow = {
  hms_patient_id: "PA-4",
  name: "Meera",
  mobile: "95****1111",
  status: "PENDING",
  registered_at: "2026-07-16T10:00:00Z",
  consented: true,
};

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

  it("badges an already-consented row and disables its action", async () => {
    (api.receptionRegistrations as any).mockResolvedValue([consentedRow]);
    render(<Reception />);

    await waitFor(() => expect(screen.getByText("Meera")).toBeInTheDocument());
    expect(screen.getByText(/already consented/i)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /send code/i })).toBeDisabled();
  });

  // The regression test for the trap: the queue re-polls every 5s and hands back a
  // fresh `rows` array each time. Arming the hide-timer per render would reset it
  // on every poll, so a 15s timer inside a 5s poll would never fire and the row
  // would never disappear. Two polls must land inside the window without resetting it.
  it("drops a consented row after 15s, surviving intervening polls", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    (api.receptionRegistrations as any).mockResolvedValue([consentedRow]);
    render(<Reception />);

    await waitFor(() => expect(screen.getByText("Meera")).toBeInTheDocument());

    // t+10s: two 5s polls have landed. The row must still be here.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10_000);
    });
    expect(screen.getByText("Meera")).toBeInTheDocument();

    // t+16s: past the 15s window.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(6_000);
    });
    await waitFor(() => expect(screen.queryByText("Meera")).not.toBeInTheDocument());

    vi.useRealTimers();
  });

  it("keeps a not-yet-consented row on the board indefinitely", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    (api.receptionRegistrations as any).mockResolvedValue([rows[0]]);
    render(<Reception />);

    await waitFor(() => expect(screen.getByText("Asha")).toBeInTheDocument());
    await act(async () => {
      await vi.advanceTimersByTimeAsync(30_000);
    });
    expect(screen.getByText("Asha")).toBeInTheDocument();

    vi.useRealTimers();
  });
});
