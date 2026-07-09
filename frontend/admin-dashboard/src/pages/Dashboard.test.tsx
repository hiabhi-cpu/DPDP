import { describe, it, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { Dashboard } from "./Dashboard";

vi.mock("../api/client", () => ({
  api: {
    getStats: vi.fn().mockResolvedValue({
      consents: { active: 128, withdrawn: 14, total_patients: 142 },
      by_purpose: [{ purpose: "treatment", active: 120, withdrawn: 6 }],
      activity: { window_days: 30, captures: 51, withdrawals: 9, renewals: 3 },
      emergency: { overrides: 7 },
    }),
    getEmergencyPending: vi.fn().mockResolvedValue({ pending: [], total: 2 }),
  },
}));

// Recharts needs a sized container in jsdom; stub ResizeObserver.
vi.stubGlobal("ResizeObserver", class { observe() {} unobserve() {} disconnect() {} });

describe("Dashboard", () => {
  it("renders stat tiles from the stats + pending endpoints", async () => {
    render(<Dashboard />);
    expect(await screen.findByText("128")).toBeInTheDocument();  // active
    expect(await screen.findByText("14")).toBeInTheDocument();   // withdrawn
    expect(await screen.findByText("7")).toBeInTheDocument();    // overrides
    expect(await screen.findByText("2")).toBeInTheDocument();    // pending (from emergency)
  });
});
