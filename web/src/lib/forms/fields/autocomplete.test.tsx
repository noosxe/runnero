import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { TextField } from "./text-field";
import { PasswordField } from "./password-field";
import { useAppForm } from "../use-app-form";
import { formContext } from "../contexts";

/** Harness mounting bound fields inside the app form providers (docs/30 §5.4). */
function FieldHarness({
  passwordAutoComplete,
  textAutoComplete,
}: {
  passwordAutoComplete?: string;
  textAutoComplete?: string;
}) {
  const form = useAppForm({
    defaultValues: { username: "", password: "" },
  });
  return (
    <formContext.Provider value={form}>
      <form.AppField name="username">
        {() => <TextField label="Username" id="t-user" autoComplete={textAutoComplete} />}
      </form.AppField>
      <form.AppField name="password">
        {() => <PasswordField label="Password" id="t-pass" autoComplete={passwordAutoComplete} />}
      </form.AppField>
    </formContext.Provider>
  );
}

describe("form autocomplete hints (docs/36 §5.5)", () => {
  it("TextField forwards autoComplete to the input", () => {
    render(<FieldHarness textAutoComplete="username" />);
    expect(screen.getByLabelText("Username")).toHaveAttribute("autocomplete", "username");
  });

  it("PasswordField forwards autoComplete to the input", () => {
    render(<FieldHarness passwordAutoComplete="current-password" />);
    expect(screen.getByLabelText("Password")).toHaveAttribute("autocomplete", "current-password");
  });

  it("omitted autoComplete stays off the input", () => {
    render(<FieldHarness />);
    expect(screen.getByLabelText("Username")).not.toHaveAttribute("autocomplete");
  });
});
