import { describe, it, expect } from "vitest";
import { homePathForRole } from "./roleHome";

describe("homePathForRole", () => {
  it("sends reception to /reception", () => expect(homePathForRole("reception")).toBe("/reception"));
  it("sends admin to /", () => expect(homePathForRole("admin")).toBe("/"));
  it("sends dpo to /", () => expect(homePathForRole("dpo")).toBe("/"));
});
