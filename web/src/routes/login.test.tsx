import { describe, it, expect, vi, beforeEach } from "vitest";
import { createRouterMock } from "@/test/router-mock";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { LoginPage } from "./login";

let mockIsPending = false;
let mockSearch: { redirect?: string } = { redirect: "/pools" };
let mockPasskeyAvailable = false;
const mockMutateAsync = vi.fn();
const mockPasskeyMutateAsync = vi.fn();
const mockNavigate = vi.fn();
const mockSetTheme = vi.fn();
let mockTheme = "light";

vi.mock("../lib/api/query-hooks", () => ({
  useLogin: () => ({
    mutateAsync: mockMutateAsync,
    get isPending() {
      return mockIsPending;
    },
  }),
  useOnboardingStatus: () => ({
    data: { passkeyAvailable: mockPasskeyAvailable },
  }),
  usePasskeyLogin: () => ({
    mutateAsync: mockPasskeyMutateAsync,
    get isPending() {
      return mockIsPending;
    },
  }),
}));

vi.mock("@tanstack/react-router", () =>
  createRouterMock({
    useNavigate: () => mockNavigate,
    useSearch: () => mockSearch,
  }),
);

vi.mock("../hooks/use-theme", () => ({
  useTheme: () => ({
    theme: mockTheme,
    setTheme: mockSetTheme,
  }),
}));

describe("LoginPage", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockIsPending = false;
    mockSearch = { redirect: "/pools" };
    mockTheme = "light";
    mockPasskeyAvailable = false;
  });

  it("renders login form with fields and buttons", () => {
    render(<LoginPage />);

    expect(screen.getByText("Sign In to Supervisor")).toBeInTheDocument();
    expect(screen.getByLabelText("Username")).toBeInTheDocument();
    expect(screen.getByLabelText("Password")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Sign In" })).toBeInTheDocument();
  });

  it("hides the passkey button when WebAuthn is not configured", () => {
    render(<LoginPage />);

    expect(screen.queryByTestId("passkey-login-button")).not.toBeInTheDocument();
  });

  it("runs the passkey ceremony and navigates on success", async () => {
    mockPasskeyAvailable = true;
    mockPasskeyMutateAsync.mockResolvedValueOnce({ success: true });
    render(<LoginPage />);

    expect(screen.getByTestId("passkey-login-button")).toBeInTheDocument();
    fireEvent.click(screen.getByTestId("passkey-login-button"));

    await waitFor(() => expect(mockPasskeyMutateAsync).toHaveBeenCalledOnce());
    await waitFor(() => expect(mockNavigate).toHaveBeenCalledWith({ to: "/pools" }));
  });

  it("translates an unknown-credential passkey failure into guidance", async () => {
    mockPasskeyAvailable = true;
    mockPasskeyMutateAsync.mockRejectedValueOnce(new Error("bad_request: credential not found"));
    render(<LoginPage />);

    fireEvent.click(screen.getByTestId("passkey-login-button"));

    await waitFor(() =>
      expect(screen.getByText(/No passkey on this device is registered/i)).toBeInTheDocument(),
    );
    expect(mockNavigate).not.toHaveBeenCalled();
  });

  it("toggles password visibility", () => {
    render(<LoginPage />);

    const passwordInput = screen.getByLabelText("Password") as HTMLInputElement;
    expect(passwordInput.type).toBe("password");

    const toggleButton = passwordInput.parentElement?.querySelector("button");
    expect(toggleButton).toBeTruthy();

    fireEvent.click(toggleButton!);
    expect(passwordInput.type).toBe("text");

    fireEvent.click(toggleButton!);
    expect(passwordInput.type).toBe("password");
  });

  it("submits credentials and navigates to target redirect URL", async () => {
    mockMutateAsync.mockResolvedValueOnce({});
    render(<LoginPage />);

    const usernameInput = screen.getByLabelText("Username");
    const passwordInput = screen.getByLabelText("Password");

    fireEvent.change(usernameInput, { target: { value: "superadmin" } });
    fireEvent.change(passwordInput, { target: { value: "secret123" } });

    const submitBtn = screen.getByRole("button", { name: "Sign In" });
    fireEvent.click(submitBtn);

    await waitFor(() => {
      expect(mockMutateAsync).toHaveBeenCalledWith({
        username: "superadmin",
        password: "secret123",
      });
      expect(mockNavigate).toHaveBeenCalledWith({ to: "/pools" });
    });
  });

  it("falls back to root '/' if redirect is an open-redirect or empty", async () => {
    mockSearch = { redirect: "https://evil.com/phishing" };
    mockMutateAsync.mockResolvedValueOnce({});
    render(<LoginPage />);

    fireEvent.change(screen.getByLabelText("Password"), { target: { value: "secret123" } });
    fireEvent.click(screen.getByRole("button", { name: "Sign In" }));

    await waitFor(() => {
      expect(mockNavigate).toHaveBeenCalledWith({ to: "/" });
    });
  });

  it("renders disabled state when login mutation is pending", () => {
    mockIsPending = true;
    render(<LoginPage />);

    const submitBtn = screen.getByRole("button", { name: "Signing in..." });
    expect(submitBtn).toBeDisabled();
  });

  it("handles theme switcher buttons", () => {
    const { rerender } = render(<LoginPage />);

    const darkBtn = screen.getByRole("button", { name: "Dark" });
    fireEvent.click(darkBtn);
    expect(mockSetTheme).toHaveBeenCalledWith("dark");
    mockTheme = "dark";
    rerender(<LoginPage />);

    const systemBtn = screen.getByRole("button", { name: "System" });
    fireEvent.click(systemBtn);
    expect(mockSetTheme).toHaveBeenCalledWith("system");
    mockTheme = "system";
    rerender(<LoginPage />);

    const lightBtn = screen.getByRole("button", { name: "Light" });
    fireEvent.click(lightBtn);
    expect(mockSetTheme).toHaveBeenCalledWith("light");
  });

  it("displays error banner when authentication fails", async () => {
    mockMutateAsync.mockRejectedValueOnce(new Error("invalid credentials provided"));
    render(<LoginPage />);

    fireEvent.change(screen.getByLabelText("Password"), { target: { value: "badpass" } });

    const submitBtn = screen.getByRole("button", { name: "Sign In" });
    fireEvent.click(submitBtn);

    await waitFor(() => {
      expect(screen.getByText("invalid credentials provided")).toBeInTheDocument();
    });
  });

  it("surfaces an inline error on blur for an empty password (RUN-222)", () => {
    render(<LoginPage />);

    const passwordInput = screen.getByLabelText("Password");
    fireEvent.focus(passwordInput);
    fireEvent.blur(passwordInput);

    // Empty password violates the wire rule (min_len) — the evaluation runs on
    // blur and the inline error carries the uniform alert markup.
    const inlineError = document.querySelector('[data-testid="form-error"]');
    expect(inlineError).not.toBeNull();
    expect(passwordInput).toHaveAttribute("aria-invalid", "true");

    // Filling the field and blurring again clears the error.
    fireEvent.change(passwordInput, { target: { value: "secret123" } });
    fireEvent.blur(passwordInput);
    expect(document.querySelector('[data-testid="form-error"]')).toBeNull();
  });

  it("blocks submission on an empty password without calling the API (RUN-222)", () => {
    render(<LoginPage />);

    fireEvent.change(screen.getByLabelText("Username"), { target: { value: "admin" } });
    // Password left empty.
    fireEvent.click(screen.getByRole("button", { name: "Sign In" }));

    expect(mockMutateAsync).not.toHaveBeenCalled();
    expect(mockNavigate).not.toHaveBeenCalled();
    expect(document.querySelector('[data-testid="form-error"]')).not.toBeNull();
  });
});
