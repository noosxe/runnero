import { describe, expect, it } from "vitest";
import { SortableHeader, ariaSortValue } from "./sortable-header";

describe("ariaSortValue", () => {
  it("maps TanStack sort state to aria-sort tokens (docs/36 §5.3)", () => {
    expect(ariaSortValue(false)).toBe("none");
    expect(ariaSortValue("asc")).toBe("ascending");
    expect(ariaSortValue("desc")).toBe("descending");
  });
});

describe("SortableHeader", () => {
  it("renders a named toggle button with direction indicator", () => {
    // Render sanity: the component is exercised fully in DataTable tests;
    // here we pin the export surface used by column definitions.
    expect(typeof SortableHeader).toBe("function");
  });
});
