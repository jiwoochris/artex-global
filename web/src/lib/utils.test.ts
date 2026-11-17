import { describe, expect, it } from "vitest";

import { formatCurrency, getInitials } from "./utils";

describe("getInitials", () => {
  it("takes the first letter of each word, uppercased", () => {
    expect(getInitials("john doe")).toBe("JD");
    expect(getInitials("  Acme   Corp ")).toBe("AC");
    expect(getInitials("hacker")).toBe("H");
  });

  it("falls back to '?' for empty or non-string input", () => {
    expect(getInitials("")).toBe("?");
    expect(getInitials("   ")).toBe("?");
    expect(getInitials(undefined as unknown as string)).toBe("?");
  });
});

describe("formatCurrency", () => {
  it("formats USD with two decimals by default", () => {
    expect(formatCurrency(1234.5)).toBe("$1,234.50");
  });

  it("honours noDecimals and locale overrides", () => {
    expect(formatCurrency(1234.56, { noDecimals: true })).toBe("$1,235");
    expect(formatCurrency(1000, { currency: "EUR", locale: "de-DE" })).toContain("1.000,00");
  });
});
