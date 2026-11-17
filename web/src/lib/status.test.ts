import { describe, expect, it } from "vitest";

import { statusMeta } from "./status";

describe("statusMeta", () => {
  it("maps each domain's status to its tone", () => {
    expect(statusMeta("severity", "critical").tone).toBe("rose");
    expect(statusMeta("severity", "high").tone).toBe("red");
    expect(statusMeta("finding", "resolved").tone).toBe("green");
    expect(statusMeta("task", "failed").tone).toBe("red");
  });

  it("falls back to the neutral tone for unknown statuses", () => {
    expect(statusMeta("task", "does-not-exist").tone).toBe("neutral");
    expect(statusMeta("delivery", "queued").tone).toBe("neutral");
  });
});
