import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { OnboardingPage } from "./onboarding";

const mockSetupAdmin = vi.fn();
const mockCreateAuthProfile = vi.fn();
const mockSetAppSetting = vi.fn();
const mockCreatePool = vi.fn();
const mockCompleteOnboarding = vi.fn();
const mockNavigate = vi.fn();

let mockOnboardingStatus: {
  adminCreated: boolean;
  authProfileExists: boolean;
  poolExists: boolean;
  setupComplete: boolean;
  hostArch?: string;
  hostOs?: string;
} = {
  adminCreated: false,
  authProfileExists: false,
  poolExists: false,
  setupComplete: false,
  hostArch: "arm64",
  hostOs: "linux",
};

let mockSession: { username: string; isAdmin: boolean; hostArch?: string; hostOs?: string } | null =
  null;
const mockLogin = vi.fn();

vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => mockNavigate,
}));

vi.mock("../lib/api/query-hooks", () => ({
  useOnboardingStatus: () => ({
    data: mockOnboardingStatus,
    isLoading: false,
  }),
  useSession: () => ({
    data: mockSession,
    isLoading: false,
  }),
  useLogin: () => ({
    mutateAsync: mockLogin,
    isPending: false,
  }),
  useSetupAdmin: () => ({
    mutateAsync: mockSetupAdmin,
    isPending: false,
  }),
  useCreateAuthProfile: () => ({
    mutateAsync: mockCreateAuthProfile,
    isPending: false,
  }),
  useSetAppSetting: () => ({
    mutateAsync: mockSetAppSetting,
    isPending: false,
  }),
  useCreatePool: () => ({
    mutateAsync: mockCreatePool,
    isPending: false,
  }),
  useCompleteOnboarding: () => ({
    mutateAsync: mockCompleteOnboarding,
    isPending: false,
  }),
}));

describe("OnboardingPage (Full 5 Steps)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockSession = null;
    mockOnboardingStatus = {
      adminCreated: false,
      authProfileExists: false,
      poolExists: false,
      setupComplete: false,
      hostArch: "arm64",
      hostOs: "linux",
    };
  });

  it("suggests amd64 runner labels when status hostArch is amd64", () => {
    mockOnboardingStatus = {
      adminCreated: true,
      authProfileExists: true,
      poolExists: false,
      setupComplete: false,
      hostArch: "amd64",
      hostOs: "linux",
    };
    mockSession = { username: "admin", isAdmin: true, hostArch: "amd64", hostOs: "linux" };

    render(<OnboardingPage />);

    const labelsInput = screen.getByLabelText("Runner Labels") as HTMLInputElement;
    expect(labelsInput.value).toBe("self-hosted,linux,amd64");
  });

  it("renders Step 1 for uninitialized supervisor", () => {
    render(<OnboardingPage />);

    expect(screen.getByText("System Onboarding")).toBeInTheDocument();
    expect(screen.getByText("Step 1 of 5: Create Master Administrator")).toBeInTheDocument();
    expect(screen.getByLabelText("Admin Username")).toHaveValue("admin");
  });

  it("enforces password length and confirmation in Step 1", async () => {
    render(<OnboardingPage />);

    const passwordInput = screen.getByLabelText("Password (min 10 characters)");
    const confirmInput = screen.getByLabelText("Confirm Password");
    const nextBtn = screen.getByRole("button", { name: /Next: Git Provider/i });

    // Short password
    fireEvent.change(passwordInput, { target: { value: "short" } });
    fireEvent.change(confirmInput, { target: { value: "short" } });
    fireEvent.click(nextBtn);

    await waitFor(() => {
      expect(screen.getByText("Password must be at least 10 characters long")).toBeInTheDocument();
    });

    // Mismatched passwords
    fireEvent.change(passwordInput, { target: { value: "validpassword1" } });
    fireEvent.change(confirmInput, { target: { value: "validpassword2" } });
    fireEvent.click(nextBtn);

    await waitFor(() => {
      expect(screen.getByText("Passwords do not match")).toBeInTheDocument();
    });
  });

  it("advances through Steps 1 to 5 and launches supervisor", async () => {
    mockSetupAdmin.mockResolvedValueOnce({});
    mockCreateAuthProfile.mockResolvedValueOnce({ profile: { id: 42n } });
    mockSetAppSetting.mockResolvedValue({});
    mockCreatePool.mockResolvedValueOnce({ pool: { id: 101n } });

    render(<OnboardingPage />);

    // --- STEP 1: Admin Setup ---
    fireEvent.change(screen.getByLabelText("Password (min 10 characters)"), {
      target: { value: "longenoughpass123" },
    });
    fireEvent.change(screen.getByLabelText("Confirm Password"), {
      target: { value: "longenoughpass123" },
    });
    fireEvent.click(screen.getByRole("button", { name: /Next: Git Provider/i }));

    await waitFor(() => {
      expect(mockSetupAdmin).toHaveBeenCalledWith({
        username: "admin",
        password: "longenoughpass123",
      });
      expect(screen.getByText("Step 2 of 5: Connect Git Provider")).toBeInTheDocument();
    });

    // --- STEP 2: Git Provider Setup ---
    fireEvent.change(screen.getByLabelText("Personal Access Token (PAT)"), {
      target: { value: "ghp_testtoken12345" },
    });
    fireEvent.click(screen.getByRole("button", { name: /Next: Safeguards/i }));

    await waitFor(() => {
      expect(mockCreateAuthProfile).toHaveBeenCalled();
      expect(screen.getByText("Step 3 of 5: Global Scaling Safeguards")).toBeInTheDocument();
    });

    // --- STEP 3: Safeguards Setup ---
    fireEvent.click(screen.getByRole("button", { name: /Next: Initial Pool/i }));

    await waitFor(() => {
      expect(mockSetAppSetting).toHaveBeenCalled();
      expect(screen.getByText("Step 4 of 5: Initial Runner Pool Setup")).toBeInTheDocument();
    });

    // --- STEP 4: Initial Pool Setup ---
    expect(screen.getByLabelText("Pool Name")).toHaveValue("default-pool");
    expect(screen.getByLabelText("Repository / Organization URL")).toBeInTheDocument();

    // Enable Renovate toggle
    const renovateCheckbox = screen.getByRole("checkbox", {
      name: "Enable Renovate Dependency Automation",
    });
    fireEvent.click(renovateCheckbox);
    expect(screen.getByLabelText("Cron Schedule")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: /Next: Review & Launch/i }));

    await waitFor(() => {
      expect(screen.getByText("Step 5 of 5: Review & Launch Supervisor")).toBeInTheDocument();
      expect(screen.getByText("Ready to Launch")).toBeInTheDocument();
    });

    // --- STEP 5: Confirm & Launch ---
    const launchBtn = screen.getByRole("button", { name: /Confirm & Launch Supervisor/i });
    fireEvent.click(launchBtn);

    await waitFor(() => {
      expect(mockCreatePool).toHaveBeenCalledWith(
        expect.objectContaining({
          pool: expect.objectContaining({
            name: "default-pool",
            provider: "github",
            authProfileId: 42n,
            allowDocker: true,
            renovate: expect.objectContaining({
              enabled: true,
              cronSchedule: "0 2 * * *",
            }),
          }),
        }),
      );
      expect(mockNavigate).toHaveBeenCalledWith({ to: "/" });
    });
  });

  it("locks allowDocker to enabled when Gitea or Forgejo provider is selected", async () => {
    mockSetupAdmin.mockResolvedValueOnce({});
    mockCreateAuthProfile.mockResolvedValueOnce({ profile: { id: 7n } });
    mockSetAppSetting.mockResolvedValue({});

    render(<OnboardingPage />);

    // Step 1
    fireEvent.change(screen.getByLabelText("Password (min 10 characters)"), {
      target: { value: "longenoughpass123" },
    });
    fireEvent.change(screen.getByLabelText("Confirm Password"), {
      target: { value: "longenoughpass123" },
    });
    fireEvent.click(screen.getByRole("button", { name: /Next: Git Provider/i }));

    await waitFor(() => {
      expect(screen.getByText("Step 2 of 5: Connect Git Provider")).toBeInTheDocument();
    });

    // Select Gitea PAT
    fireEvent.click(screen.getByRole("button", { name: "Gitea PAT" }));
    fireEvent.change(screen.getByLabelText("Personal Access Token (PAT)"), {
      target: { value: "gitea_token_abc" },
    });
    fireEvent.click(screen.getByRole("button", { name: /Next: Safeguards/i }));

    await waitFor(() => {
      expect(screen.getByText("Step 3 of 5: Global Scaling Safeguards")).toBeInTheDocument();
    });

    // Step 3 -> Step 4
    fireEvent.click(screen.getByRole("button", { name: /Next: Initial Pool/i }));

    await waitFor(() => {
      expect(screen.getByText("Step 4 of 5: Initial Runner Pool Setup")).toBeInTheDocument();
    });

    // Verify Docker policy lock per docs/05 §4
    const dockerCheckbox = screen.getByRole("checkbox", {
      name: "Allow Docker in Container",
    });
    expect(dockerCheckbox).toHaveAttribute("aria-checked", "true");
    expect(dockerCheckbox).toHaveAttribute("data-disabled");
    expect(screen.getByText(/Locked to Enabled for GITEA runners/i)).toBeInTheDocument();
  });

  it("displays error banner if admin setup API call fails in Step 1", async () => {
    mockSetupAdmin.mockRejectedValueOnce(new Error("Database write failure"));

    render(<OnboardingPage />);

    fireEvent.change(screen.getByLabelText("Password (min 10 characters)"), {
      target: { value: "validpassword123" },
    });
    fireEvent.change(screen.getByLabelText("Confirm Password"), {
      target: { value: "validpassword123" },
    });
    fireEvent.click(screen.getByRole("button", { name: /Next: Git Provider/i }));

    await waitFor(() => {
      expect(screen.getByText("Database write failure")).toBeInTheDocument();
    });
  });

  it("resumes at Step 2 if admin is already created", () => {
    mockOnboardingStatus = {
      adminCreated: true,
      authProfileExists: false,
      poolExists: false,
      setupComplete: false,
    };

    mockSession = { username: "admin", isAdmin: true };
    render(<OnboardingPage />);

    expect(screen.getByText("Step 2 of 5: Connect Git Provider")).toBeInTheDocument();
  });

  it("renders administrator authentication form if admin is created but no session is active, and authenticates successfully", async () => {
    mockOnboardingStatus = {
      adminCreated: true,
      authProfileExists: false,
      poolExists: false,
      setupComplete: false,
    };
    mockSession = null;
    mockLogin.mockResolvedValueOnce({ success: true, username: "admin" });

    render(<OnboardingPage />);

    expect(
      screen.getByText("Step 1 of 5: Master Administrator Authentication"),
    ).toBeInTheDocument();
    expect(screen.getByLabelText("Admin Password")).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText("Admin Password"), {
      target: { value: "mypassword123" },
    });
    fireEvent.click(screen.getByRole("button", { name: /Log In to Continue Setup/i }));

    await waitFor(() => {
      expect(mockLogin).toHaveBeenCalledWith({
        username: "admin",
        password: "mypassword123",
      });
    });
  });

  it("prevents skipping to dashboard when admin is created but unauthenticated", async () => {
    mockOnboardingStatus = {
      adminCreated: true,
      authProfileExists: false,
      poolExists: false,
      setupComplete: false,
    };
    mockSession = null;

    render(<OnboardingPage />);

    fireEvent.click(screen.getByRole("button", { name: /Skip to Dashboard/i }));

    await waitFor(() => {
      expect(
        screen.getByText("Please log in with administrator credentials first to complete setup."),
      ).toBeInTheDocument();
    });
    expect(mockCompleteOnboarding).not.toHaveBeenCalled();
  });

  it("supports navigating back between steps using Back button", async () => {
    mockSetupAdmin.mockResolvedValueOnce({});

    render(<OnboardingPage />);

    fireEvent.change(screen.getByLabelText("Password (min 10 characters)"), {
      target: { value: "validpassword123" },
    });
    fireEvent.change(screen.getByLabelText("Confirm Password"), {
      target: { value: "validpassword123" },
    });
    fireEvent.click(screen.getByRole("button", { name: /Next: Git Provider/i }));

    await waitFor(() => {
      expect(screen.getByText("Step 2 of 5: Connect Git Provider")).toBeInTheDocument();
    });

    const backBtn = screen.getByRole("button", { name: /Back/i });
    fireEvent.click(backBtn);

    expect(screen.getByText("Step 1 of 5: Create Master Administrator")).toBeInTheDocument();
  });

  it("validates that min idle runners cannot exceed max concurrency in Step 4", async () => {
    mockSetupAdmin.mockResolvedValueOnce({});
    mockCreateAuthProfile.mockResolvedValueOnce({ profile: { id: 10n } });
    mockSetAppSetting.mockResolvedValue({});

    render(<OnboardingPage />);

    // Step 1
    fireEvent.change(screen.getByLabelText("Password (min 10 characters)"), {
      target: { value: "longenoughpass123" },
    });
    fireEvent.change(screen.getByLabelText("Confirm Password"), {
      target: { value: "longenoughpass123" },
    });
    fireEvent.click(screen.getByRole("button", { name: /Next: Git Provider/i }));

    await waitFor(() => {
      expect(screen.getByText("Step 2 of 5: Connect Git Provider")).toBeInTheDocument();
    });

    // Step 2
    fireEvent.change(screen.getByLabelText("Personal Access Token (PAT)"), {
      target: { value: "ghp_token12345" },
    });
    fireEvent.click(screen.getByRole("button", { name: /Next: Safeguards/i }));

    await waitFor(() => {
      expect(screen.getByText("Step 3 of 5: Global Scaling Safeguards")).toBeInTheDocument();
    });

    // Step 3
    fireEvent.click(screen.getByRole("button", { name: /Next: Initial Pool/i }));

    await waitFor(() => {
      expect(screen.getByText("Step 4 of 5: Initial Runner Pool Setup")).toBeInTheDocument();
    });

    // Step 4: set minIdle > maxConcurrency
    fireEvent.change(screen.getByLabelText("Min Idle Runners"), { target: { value: "10" } });
    fireEvent.change(screen.getByLabelText("Max Concurrency"), { target: { value: "2" } });
    fireEvent.click(screen.getByRole("button", { name: /Next: Review & Launch/i }));

    await waitFor(() => {
      expect(
        screen.getByText("Min idle runners cannot exceed max concurrency"),
      ).toBeInTheDocument();
    });
  });

  it("displays error banner when launch supervisor fails in Step 5", async () => {
    mockSetupAdmin.mockResolvedValueOnce({});
    mockCreateAuthProfile.mockResolvedValueOnce({ profile: { id: 10n } });
    mockSetAppSetting.mockResolvedValue({});
    mockCreatePool.mockRejectedValueOnce(new Error("Docker daemon communication timeout"));

    render(<OnboardingPage />);

    // Step 1
    fireEvent.change(screen.getByLabelText("Password (min 10 characters)"), {
      target: { value: "longenoughpass123" },
    });
    fireEvent.change(screen.getByLabelText("Confirm Password"), {
      target: { value: "longenoughpass123" },
    });
    fireEvent.click(screen.getByRole("button", { name: /Next: Git Provider/i }));

    await waitFor(() => {
      expect(screen.getByText("Step 2 of 5: Connect Git Provider")).toBeInTheDocument();
    });

    // Step 2
    fireEvent.change(screen.getByLabelText("Personal Access Token (PAT)"), {
      target: { value: "ghp_token12345" },
    });
    fireEvent.click(screen.getByRole("button", { name: /Next: Safeguards/i }));

    await waitFor(() => {
      expect(screen.getByText("Step 3 of 5: Global Scaling Safeguards")).toBeInTheDocument();
    });

    // Step 3
    fireEvent.click(screen.getByRole("button", { name: /Next: Initial Pool/i }));

    await waitFor(() => {
      expect(screen.getByText("Step 4 of 5: Initial Runner Pool Setup")).toBeInTheDocument();
    });

    // Step 4
    fireEvent.click(screen.getByRole("button", { name: /Next: Review & Launch/i }));

    await waitFor(() => {
      expect(screen.getByText("Step 5 of 5: Review & Launch Supervisor")).toBeInTheDocument();
    });

    // Step 5: Launch
    fireEvent.click(screen.getByRole("button", { name: /Confirm & Launch Supervisor/i }));

    await waitFor(() => {
      expect(screen.getByText("Docker daemon communication timeout")).toBeInTheDocument();
    });
  });

  it("allows skipping directly to dashboard after Step 1 admin setup", async () => {
    mockSetupAdmin.mockResolvedValueOnce({});
    mockCompleteOnboarding.mockResolvedValueOnce({});

    render(<OnboardingPage />);

    // Step 1
    fireEvent.change(screen.getByLabelText("Password (min 10 characters)"), {
      target: { value: "longenoughpass123" },
    });
    fireEvent.change(screen.getByLabelText("Confirm Password"), {
      target: { value: "longenoughpass123" },
    });
    fireEvent.click(screen.getByRole("button", { name: /Next: Git Provider/i }));

    await waitFor(() => {
      expect(screen.getByText("Step 2 of 5: Connect Git Provider")).toBeInTheDocument();
    });

    const skipHeaderBtn = screen.getByRole("button", { name: /Skip to Dashboard/i });
    expect(skipHeaderBtn).toBeInTheDocument();
    fireEvent.click(skipHeaderBtn);

    await waitFor(() => {
      expect(mockCompleteOnboarding).toHaveBeenCalled();
      expect(mockNavigate).toHaveBeenCalledWith({ to: "/" });
    });
  });

  it("allows skipping Step 2, shows Step 4 prerequisite banner, and skips pool to finish setup", async () => {
    mockSetupAdmin.mockResolvedValueOnce({});
    mockCompleteOnboarding.mockResolvedValueOnce({});

    render(<OnboardingPage />);

    // Step 1
    fireEvent.change(screen.getByLabelText("Password (min 10 characters)"), {
      target: { value: "longenoughpass123" },
    });
    fireEvent.change(screen.getByLabelText("Confirm Password"), {
      target: { value: "longenoughpass123" },
    });
    fireEvent.click(screen.getByRole("button", { name: /Next: Git Provider/i }));

    await waitFor(() => {
      expect(screen.getByText("Step 2 of 5: Connect Git Provider")).toBeInTheDocument();
    });

    // Step 2: Skip
    fireEvent.click(screen.getByRole("button", { name: /Skip this step/i }));

    await waitFor(() => {
      expect(screen.getByText("Step 3 of 5: Global Scaling Safeguards")).toBeInTheDocument();
    });

    // Step 3: Keep defaults & continue
    fireEvent.click(screen.getByRole("button", { name: /Keep defaults & continue/i }));

    await waitFor(() => {
      expect(screen.getByText("Step 4 of 5: Initial Runner Pool Setup")).toBeInTheDocument();
      expect(screen.getByText("Git Authentication Profile Required")).toBeInTheDocument();
    });

    // Step 4: Skip Pool Setup & Review
    fireEvent.click(screen.getByRole("button", { name: /Skip Pool Setup & Review/i }));

    await waitFor(() => {
      expect(screen.getByText("Step 5 of 5: Review & Launch Supervisor")).toBeInTheDocument();
      expect(screen.getByText(/Skipped — not configured/i)).toBeInTheDocument();
      expect(screen.getByText(/Skipped — no pool created/i)).toBeInTheDocument();
      expect(screen.getByText("Ready to Finish Setup")).toBeInTheDocument();
    });

    // Step 5: Finish & Open Dashboard
    fireEvent.click(screen.getByRole("button", { name: /Finish & Open Dashboard/i }));

    await waitFor(() => {
      expect(mockCreatePool).not.toHaveBeenCalled();
      expect(mockCompleteOnboarding).toHaveBeenCalled();
      expect(mockNavigate).toHaveBeenCalledWith({ to: "/" });
    });
  });

  it("allows creating Git profile, skipping pool setup, and finishing onboarding", async () => {
    mockSetupAdmin.mockResolvedValueOnce({});
    mockCreateAuthProfile.mockResolvedValueOnce({ profile: { id: 10n } });
    mockCompleteOnboarding.mockResolvedValueOnce({});

    render(<OnboardingPage />);

    // Step 1: Admin
    fireEvent.change(screen.getByLabelText("Password (min 10 characters)"), {
      target: { value: "longenoughpass123" },
    });
    fireEvent.change(screen.getByLabelText("Confirm Password"), {
      target: { value: "longenoughpass123" },
    });
    fireEvent.click(screen.getByRole("button", { name: /Next: Git Provider/i }));

    await waitFor(() => {
      expect(screen.getByText("Step 2 of 5: Connect Git Provider")).toBeInTheDocument();
    });

    // Step 2: Configure Git profile
    fireEvent.change(screen.getByLabelText("Personal Access Token (PAT)"), {
      target: { value: "ghp_validtoken123" },
    });
    fireEvent.click(screen.getByRole("button", { name: /Next: Safeguards/i }));

    await waitFor(() => {
      expect(screen.getByText("Step 3 of 5: Global Scaling Safeguards")).toBeInTheDocument();
    });

    // Step 3: Safeguards -> Next
    fireEvent.click(screen.getByRole("button", { name: /Keep defaults & continue/i }));

    await waitFor(() => {
      expect(screen.getByText("Step 4 of 5: Initial Runner Pool Setup")).toBeInTheDocument();
      // Should show the pool form, not the prerequisite warning
      expect(screen.queryByText("Git Authentication Profile Required")).not.toBeInTheDocument();
      expect(screen.getByLabelText("Pool Name")).toBeInTheDocument();
    });

    // Step 4: Skip pool creation
    fireEvent.click(screen.getByRole("button", { name: /Skip this step/i }));

    await waitFor(() => {
      expect(screen.getByText("Step 5 of 5: Review & Launch Supervisor")).toBeInTheDocument();
      // Git profile should NOT be skipped
      expect(screen.queryByText(/Skipped — not configured/i)).not.toBeInTheDocument();
      expect(screen.getByText("github-primary")).toBeInTheDocument();
      // Initial pool should be skipped
      expect(screen.getByText(/Skipped — no pool created/i)).toBeInTheDocument();
      expect(screen.getByText("Ready to Finish Setup")).toBeInTheDocument();
    });

    // Step 5: Finish
    fireEvent.click(screen.getByRole("button", { name: /Finish & Open Dashboard/i }));

    await waitFor(() => {
      expect(mockCreatePool).not.toHaveBeenCalled();
      expect(mockCompleteOnboarding).toHaveBeenCalled();
      expect(mockNavigate).toHaveBeenCalledWith({ to: "/" });
    });
  });

  it("shows GitHub App installation prompt in Step 2 when created app is uninstalled", async () => {
    mockSetupAdmin.mockResolvedValueOnce({});
    mockCreateAuthProfile.mockResolvedValueOnce({
      profile: {
        id: 15n,
        name: "test-app-profile",
        authMethod: "github_app",
        installUrl: "https://github.com/apps/test-app-profile/installations/new",
        installationsCount: 0,
      },
    });

    render(<OnboardingPage />);

    // Step 1: Admin
    fireEvent.change(screen.getByLabelText("Password (min 10 characters)"), {
      target: { value: "validpassword123" },
    });
    fireEvent.change(screen.getByLabelText("Confirm Password"), {
      target: { value: "validpassword123" },
    });
    fireEvent.click(screen.getByRole("button", { name: /Next: Git Provider/i }));

    await waitFor(() => {
      expect(screen.getByText("Step 2 of 5: Connect Git Provider")).toBeInTheDocument();
    });

    // Step 2: Switch to GitHub App
    fireEvent.click(screen.getByRole("button", { name: "GitHub App" }));
    fireEvent.change(screen.getByLabelText("GitHub App ID"), { target: { value: "9988" } });
    fireEvent.change(screen.getByLabelText("Private Key PEM"), {
      target: { value: "-----BEGIN RSA PRIVATE KEY-----\nMIIE...\n-----END RSA PRIVATE KEY-----" },
    });
    fireEvent.click(screen.getByRole("button", { name: /Next: Safeguards/i }));

    // Expect installation prompt in Step 2
    await waitFor(() => {
      expect(
        screen.getByText("Step 2 of 5: Install GitHub App on Your Account"),
      ).toBeInTheDocument();
    });

    expect(screen.getByText("Action Required: Install App in GitHub")).toBeInTheDocument();
    const installLink = screen.getByRole("link", { name: /Install GitHub App on GitHub/i });
    expect(installLink).toBeInTheDocument();
    expect(installLink).toHaveAttribute(
      "href",
      "https://github.com/apps/test-app-profile/installations/new",
    );
    expect(installLink).toHaveAttribute("target", "_blank");

    // Click Continue to Safeguards
    fireEvent.click(screen.getByRole("button", { name: /Continue: Safeguards/i }));

    await waitFor(() => {
      expect(screen.getByText("Step 3 of 5: Global Scaling Safeguards")).toBeInTheDocument();
    });
  });
});
