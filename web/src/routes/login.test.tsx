import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { LoginPage } from "./login";

let mockIsPending = false;
let mockSearch: { redirect?: string } = { redirect: "/pools" };
const mockMutateAsync = vi.fn();
const mockNavigate = vi.fn();
const mockSetTheme = vi.fn();

vi.mock("../lib/api/query-hooks", () => ({
  useLogin: () => ({
    mutateAsync: mockMutateAsync,
    get isPending() {
      return mockIsPending;
    },
  }),
}));

vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => mockNavigate,
  useSearch: () => mockSearch,
}));

vi.mock("../hooks/use-theme", () => ({
  useTheme: () => ({
    theme: "light",
    setTheme: mockSetTheme,
  }),
}));

describe("LoginPage", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockIsPending = false;
    mockSearch = { redirect: "/pools" };
  });

  it("renders login form with fields and buttons", () => {
    render(<LoginPage />);

    expect(screen.getByText("Sign In to Supervisor")).toBeInTheDocument();
    expect(screen.getByLabelText("Username")).toBeInTheDocument();
    expect(screen.getByLabelText("Password")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Sign In" })).toBeInTheDocument();
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
    render(<LoginPage />);

    const darkBtn = screen.getByRole("button", { name: "Dark Theme" });
    fireEvent.click(darkBtn);
    expect(mockSetTheme).toHaveBeenCalledWith("dark");

    const systemBtn = screen.getByRole("button", { name: "System Theme" });
    fireEvent.click(systemBtn);
    expect(mockSetTheme).toHaveBeenCalledWith("system");

    const lightBtn = screen.getByRole("button", { name: "Light Theme" });
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
});
