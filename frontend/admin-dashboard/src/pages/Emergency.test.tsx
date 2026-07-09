import { describe, it, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Emergency } from "./Emergency";

const item = {
  access_id: "acc-1", emergency_id: "EMRG-1", doctor_id: "D-12",
  emergency_reason: "life_threatening", clinical_note: "trauma",
  review_status: "PENDING", dpo_deadline: new Date(Date.now() + 3600e3).toISOString(),
  overdue: false, created_at: new Date().toISOString(),
};

const getPending = vi.fn().mockResolvedValue({ pending: [item], total: 1 });
const review = vi.fn().mockResolvedValue({ status: "reviewed" });
vi.mock("../api/client", () => ({ api: { getEmergencyPending: () => getPending(), reviewEmergency: (...a: unknown[]) => review(...a) } }));

describe("Emergency", () => {
  it("lists pending overrides and submits a VERIFIED review", async () => {
    render(<Emergency />);
    expect(await screen.findByText("D-12")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: /review/i }));
    await userEvent.click(await screen.findByRole("button", { name: /mark verified/i }));
    expect(review).toHaveBeenCalledWith("acc-1", "VERIFIED");
  });

  it("shows the review error inside the modal on failure and keeps it open", async () => {
    getPending.mockResolvedValueOnce({ pending: [item], total: 1 });
    review.mockRejectedValueOnce(new Error("review failed"));

    render(<Emergency />);
    expect(await screen.findByText("D-12")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: /review/i }));
    await userEvent.click(await screen.findByRole("button", { name: /mark verified/i }));

    expect(await screen.findByText("review failed")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /mark verified/i })).toBeInTheDocument();
  });
});
