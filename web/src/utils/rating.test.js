import { describe, expect, it } from "vitest";
import { formatRating } from "./rating";

describe("formatRating", () => {
  it("formats external ratings without trailing zeros", () => {
    expect(formatRating(4, 5, "ru-RU")).toBe("4/5");
    expect(formatRating(4.1, 5, "ru-RU")).toBe("4,1/5");
    expect(formatRating(4.26, 5, "ru-RU")).toBe("4,26/5");
    expect(formatRating(5, 5, "ru-RU")).toBe("5/5");
    expect(formatRating(8.52 / 2, 5, "ru-RU")).toBe("4,26/5");
    expect(formatRating(10 / 2, 5, "ru-RU")).toBe("5/5");
  });

  it("uses a decimal point for English", () => {
    expect(formatRating(4.1, 5, "en-US")).toBe("4.1/5");
  });

  it("returns null for missing ratings", () => {
    expect(formatRating(0, 5, "ru-RU")).toBeNull();
    expect(formatRating(null, 5, "ru-RU")).toBeNull();
  });
});
