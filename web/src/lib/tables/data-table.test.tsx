import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { useState } from "react";
import { createColumnHelper } from "@tanstack/react-table";
import type { SortingState } from "@tanstack/react-table";
import { DataTable, SortableHeader, useAppTable } from "./index";
import type { AppTableFeatures } from "./index";

type Row = { id: string; label: string; count: number };

const helper = createColumnHelper<AppTableFeatures, Row>();

const makeColumns = (onPick: (id: string) => void) =>
  helper.columns([
    helper.accessor("label", {
      header: "Label",
      cell: (info) => <span className="font-semibold">{info.getValue()}</span>,
    }),
    helper.accessor("count", {
      header: "Count",
      meta: { headerClassName: "text-right", cellClassName: "text-right" },
      cell: (info) => info.getValue(),
    }),
    helper.display({
      id: "actions",
      header: "Actions",
      cell: ({ row }) => (
        <button type="button" onClick={() => onPick(row.original.id)}>
          Pick
        </button>
      ),
    }),
  ]);

function Harness({
  rows,
  onPick = () => {},
  empty,
}: {
  rows: Row[];
  onPick?: (id: string) => void;
  empty?: React.ReactNode;
}) {
  const table = useAppTable({
    columns: makeColumns(onPick),
    data: rows,
    getRowId: (row) => row.id,
  });
  return <DataTable table={table} empty={empty} />;
}

const rows: Row[] = [
  { id: "a", label: "Alpha", count: 3 },
  { id: "b", label: "Beta", count: 7 },
];

const sortableColumns = () =>
  helper.columns([
    helper.accessor("count", {
      header: ({ column }) => <SortableHeader column={column}>Count</SortableHeader>,
      sortFn: "basic",
      meta: { cellClassName: "font-mono" },
    }),
    helper.accessor("label", {
      header: "Label",
    }),
  ]);

function SortingHarness() {
  const [sorting, setSorting] = useState<SortingState>([]);
  const table = useAppTable({
    columns: sortableColumns(),
    data: rows,
    getRowId: (row) => row.id,
    state: { sorting },
    onSortingChange: setSorting,
    sortDescFirst: false,
  });
  return <DataTable table={table} />;
}

const clickSort = () => fireEvent.click(screen.getByRole("button", { name: "Sort by Count" }));

const cellText = () => screen.getAllByText(/^(Alpha|Beta)$/).map((e) => e.textContent);

describe("DataTable", () => {
  it("renders headers and row content through the column defs", () => {
    render(<Harness rows={rows} />);
    expect(screen.getByRole("columnheader", { name: "Label" })).toBeTruthy();
    expect(screen.getByRole("columnheader", { name: "Count" })).toBeTruthy();
    expect(screen.getByRole("columnheader", { name: "Actions" })).toBeTruthy();
    expect(screen.getByText("Alpha")).toBeTruthy();
    expect(screen.getByText("Beta")).toBeTruthy();
    expect(screen.getByText("7")).toBeTruthy();
  });

  it("applies typed column meta classes to header and cells", () => {
    render(<Harness rows={rows} />);
    const countHeader = screen.getByRole("columnheader", { name: "Count" });
    expect(countHeader.className).toContain("text-right");
    const countCell = screen.getByText("7").closest("td");
    expect(countCell?.className).toContain("text-right");
    // columns without meta get no injected class beyond the shell default
    const labelCell = screen.getByText("Alpha").closest("td");
    expect(labelCell?.className).not.toContain("text-right");
  });

  it("renders the default empty state when there are no rows", () => {
    render(<Harness rows={[]} />);
    expect(screen.getByText("No results.")).toBeTruthy();
  });

  it("renders the provided empty slot instead of the default", () => {
    render(<Harness rows={[]} empty={<div>Nothing configured.</div>} />);
    expect(screen.getByText("Nothing configured.")).toBeTruthy();
    expect(screen.queryByText("No results.")).toBeNull();
  });

  it("wires display-column callbacks to the row original", async () => {
    const onPick = vi.fn();
    render(<Harness rows={rows} onPick={onPick} />);
    const buttons = screen.getAllByRole("button", { name: "Pick" });
    expect(buttons).toHaveLength(2);
    buttons[1]!.click();
    expect(onPick).toHaveBeenCalledWith("b");
  });
});

describe("DataTable sorting (docs/31 §4.4)", () => {
  it("toggles ascending, descending, then resets to insertion order", () => {
    render(<SortingHarness />);
    expect(cellText()).toEqual(["Alpha", "Beta"]);

    // The header button node is replaced on each state change, so always
    // re-query right before clicking.
    clickSort(); // asc
    expect(cellText()[0]).toBe("Alpha"); // count 3

    clickSort(); // desc
    expect(cellText()[0]).toBe("Beta"); // count 7

    clickSort(); // removal — back to insertion order
    expect(cellText()).toEqual(["Alpha", "Beta"]);
  });
});
