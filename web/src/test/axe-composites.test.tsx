import { describe, expect, it } from "vitest";
import { act, fireEvent, render, screen } from "@testing-library/react";
import axe from "axe-core";
import { TextField } from "@/lib/forms/fields/text-field";
import { PasswordField } from "@/lib/forms/fields/password-field";
import { CheckboxField } from "@/lib/forms/fields/checkbox-field";
import { TextareaField } from "@/lib/forms/fields/textarea-field";
import { useAppForm } from "@/lib/forms/use-app-form";
import { formContext } from "@/lib/forms/contexts";
import { DataTable, SortableHeader, useAppTable } from "@/lib/tables";
import type { AnyFieldLikeMetaBase, AnyFormApi } from "@tanstack/react-form";
import { createColumnHelper } from "@tanstack/react-table";
import type { AppTableFeatures } from "@/lib/tables";
import { LogTerminal } from "@/components/terminal/log-terminal";
import type { LogChunk } from "@/gen/api_pb";
import { Toaster, toast } from "@/components/ui/toast";

/**
 * Vitest axe gate for shared composites (docs/36 §6.2, RUN-265): the
 * semantic regressions axe can catch in jsdom start in these components —
 * form fields (default + error state), data-table (sorting + pinned first
 * column), LogTerminal controls, and the toast viewport. jsdom has no
 * layout engine, so `color-contrast` is disabled here — contrast is owned
 * by the token palette (RUN-256) and the enforcing E2E scans (§6.1).
 * Everything the wcag2a/2aa/22aa tags run in jsdom must pass: these are
 * small, controlled trees, so any violation is a real defect.
 */
async function scanA11y(container: HTMLElement): Promise<void> {
  const results = await axe.run(container, {
    runOnly: { type: "tag", values: ["wcag2a", "wcag2aa", "wcag22aa"] },
    // jsdom cannot compute rendered contrast; see file comment (docs/36 §6.2).
    rules: { "color-contrast": { enabled: false } },
  });
  const summary = results.violations.map(
    (v) =>
      `[${v.impact ?? "?"}] ${v.id}: ${v.help}\n` +
      v.nodes.map((n) => `  ↳ ${n.target.join(" ")}`).join("\n"),
  );
  expect(summary, `axe violations:\n${summary.join("\n")}`).toEqual([]);
}

// ---- form fields (docs/30 §5.6 markup contract) ---------------------------

type FieldValues = {
  username: string;
  password: string;
  remember: boolean;
  notes: string;
};

/**
 * Form instance handle the harness exposes so tests can force errors.
 * v1.33 derives meta.errors from errorMap, so tests write onBlur like
 * apply-field-errors.ts does (a direct `errors:` write would be ignored).
 */
let formRef: { setFieldMeta: AnyFormApi["setFieldMeta"] } | undefined;

// Captured outside render on purpose (the lint rule is right that render
// must not write to outer state): the test hooks in via the onReady prop.
const captureForm = (form: { setFieldMeta: AnyFormApi["setFieldMeta"] }) => {
  formRef = form;
};

const forceError = (message: string) => (prev: AnyFieldLikeMetaBase) => ({
  ...prev,
  errorMap: { ...prev.errorMap, onBlur: message },
});

function FieldsHarness({
  onReady = () => {},
}: {
  onReady?: (form: { setFieldMeta: AnyFormApi["setFieldMeta"] }) => void;
}) {
  const form = useAppForm({
    defaultValues: {
      username: "",
      password: "",
      remember: false,
      notes: "",
    } as FieldValues,
  });
  onReady(form);
  return (
    <formContext.Provider value={form}>
      <form.AppField name="username">
        {() => <TextField label="Username" id="ax-user" />}
      </form.AppField>
      <form.AppField name="password">
        {() => <PasswordField label="Password" id="ax-pass" />}
      </form.AppField>
      <form.AppField name="remember">
        {() => <CheckboxField label="Remember me" id="ax-remember" />}
      </form.AppField>
      <form.AppField name="notes">
        {() => <TextareaField label="Notes" id="ax-notes" />}
      </form.AppField>
    </formContext.Provider>
  );
}

describe("a11y composites (docs/36 §6.2)", () => {
  it("form fields pass axe in the default state", async () => {
    const { container } = render(<FieldsHarness />);
    await scanA11y(container);
  });

  it("form fields pass axe in the error state (aria-invalid + describedby)", async () => {
    const { container } = render(<FieldsHarness onReady={captureForm} />);
    await act(async () => {
      for (const name of ["username", "password", "notes"] as const) {
        formRef!.setFieldMeta(name, forceError("This field is required"));
      }
      formRef!.setFieldMeta("remember", forceError("Must be accepted"));
    });
    // The error markup the contract depends on is actually on screen.
    expect(container.querySelector("[data-invalid='true']")).not.toBeNull();
    expect(screen.getAllByRole("alert").length).toBeGreaterThan(0);
    await scanA11y(container);
  });

  it("data-table passes axe while sorted with a pinned first column", async () => {
    type Row = { id: string; label: string; count: number };
    const helper = createColumnHelper<AppTableFeatures, Row>();
    const columns = helper.columns([
      helper.accessor("label", {
        header: ({ column }) => <SortableHeader column={column}>Label</SortableHeader>,
      }),
      helper.accessor("count", { header: "Count" }),
    ]);
    function TableHarness() {
      const table = useAppTable({
        columns,
        data: [
          { id: "1", label: "beta", count: 2 },
          { id: "2", label: "alpha", count: 1 },
        ],
        getRowId: (row) => row.id,
      });
      return <DataTable table={table} pinFirst />;
    }
    const { container } = render(<TableHarness />);
    // Sort the first column so aria-sort is live, then scan.
    fireEvent.click(screen.getByRole("button", { name: "Sort by Label" }));
    expect(
      container.querySelector('[aria-sort="ascending"], [aria-sort="descending"]'),
    ).not.toBeNull();
    await scanA11y(container);
  });

  it("LogTerminal controls pass axe", async () => {
    const logs = [
      {
        timestamp: "2026-09-04T00:50:01Z",
        stream: "stdout",
        content: "line one",
      },
      {
        timestamp: "2026-09-04T00:50:02Z",
        stream: "stderr",
        content: "line two",
      },
    ] as LogChunk[];
    const { container } = render(
      <LogTerminal logs={logs} mode="live" runnerName="runnero-axe-terminal" isConnected />,
    );
    // The controls the §5.4 policy relies on are present before scanning.
    expect(screen.getByRole("log")).toBeInTheDocument();
    expect(screen.getByLabelText("Filter log output")).toBeInTheDocument();
    await scanA11y(container);
  });

  it("toast viewport passes axe with a visible toast", async () => {
    const { container } = render(<Toaster />);
    await act(async () => {
      toast.add({
        title: "Admin role required",
        description: "Your account has read-only access to this area.",
      });
    });
    expect(screen.getByText("Admin role required")).toBeInTheDocument();
    await scanA11y(container);
  });
});
