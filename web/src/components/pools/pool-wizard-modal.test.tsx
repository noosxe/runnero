import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { PoolWizardModal } from "./pool-wizard-modal";
import type { Pool } from "../../gen/api_pb";

const mockMutateAsync = vi.fn();
let mockDiscoveredTargets: Array<{
  name: string;
  fullName: string;
  htmlUrl: string;
  description: string;
  isPrivate: boolean;
  avatarUrl: string;
}> = [];
let mockIsDiscovering = false;
let mockDiscoveryError: Error | null = null;
let mockInstallUrl = "";
let mockInstallations: Array<{
  id: bigint;
  accountLogin: string;
  accountType: string;
  htmlUrl: string;
  repositorySelection: string;
}> = [];
const mockRefetchDiscovery = vi.fn();

const mockUpdateMutateAsync = vi.fn();

vi.mock("../../lib/api/query-hooks", () => ({
  useCreatePool: () => ({
    mutateAsync: mockMutateAsync,
    isPending: false,
  }),
  useUpdatePool: () => ({
    mutateAsync: mockUpdateMutateAsync,
    isPending: false,
  }),
  useDiscoverTargets: () => ({
    data: {
      targets: mockDiscoveredTargets,
      installUrl: mockInstallUrl,
      installations: mockInstallations,
    },
    isLoading: mockIsDiscovering,
    error: mockDiscoveryError,
    refetch: mockRefetchDiscovery,
  }),
}));

describe("PoolWizardModal", () => {
  const defaultAuthProfiles = [
    { id: 10n, name: "corp-github-app", authMethod: "github-app" },
    { id: 20n, name: "internal-forgejo", authMethod: "forgejo-token" },
  ];

  beforeEach(() => {
    vi.clearAllMocks();
    mockUpdateMutateAsync.mockResolvedValue({ pool: { id: 101n } });
    mockMutateAsync.mockResolvedValue({ pool: { id: 101n } });
    mockInstallUrl = "";
    mockInstallations = [];
    mockDiscoveredTargets = [
      {
        name: "frontend-monorepo",
        fullName: "acme-corp/frontend-monorepo",
        htmlUrl: "https://github.com/acme-corp/frontend-monorepo",
        description: "Primary frontend application",
        isPrivate: true,
        avatarUrl: "",
      },
      {
        name: "backend-core",
        fullName: "acme-corp/backend-core",
        htmlUrl: "https://github.com/acme-corp/backend-core",
        description: "Core backend services",
        isPrivate: false,
        avatarUrl: "",
      },
    ];
    mockIsDiscovering = false;
    mockDiscoveryError = null;
  });

  it("does not render when isOpen is false", () => {
    const { container } = render(
      <PoolWizardModal isOpen={false} onClose={vi.fn()} authProfiles={defaultAuthProfiles} />,
    );
    expect(container.firstChild).toBeNull();
  });

  it("validates pool name slug and enforces profile selection on Step 1", () => {
    render(<PoolWizardModal isOpen={true} onClose={vi.fn()} authProfiles={defaultAuthProfiles} />);

    expect(screen.getByText("Create Runner Pool Wizard")).toBeInTheDocument();
    expect(screen.getByText("Identity & Auth")).toBeInTheDocument();

    const continueButton = screen.getByRole("button", {
      name: /Continue to Scope & Targets/i,
    });
    expect(continueButton).toBeDisabled();

    // Invalid slug with uppercase or spaces
    const nameInput = screen.getByLabelText(/Pool Name \(Slug\)/i);
    fireEvent.change(nameInput, { target: { value: "INVALID POOL" } });
    expect(continueButton).toBeDisabled();

    // Valid slug
    fireEvent.change(nameInput, { target: { value: "arm64-prod-workers" } });
    expect(continueButton).toBeEnabled();
  });

  it("supports multi-target selection and 'Select All Filtered' on Step 2", () => {
    render(<PoolWizardModal isOpen={true} onClose={vi.fn()} authProfiles={defaultAuthProfiles} />);

    // Step 1 -> Step 2
    fireEvent.change(screen.getByLabelText(/Pool Name \(Slug\)/i), {
      target: { value: "ci-pool" },
    });
    fireEvent.click(screen.getByRole("button", { name: /Continue to Scope & Targets/i }));

    expect(screen.getByText("Scope & Discovery")).toBeInTheDocument();
    expect(screen.getByText("acme-corp/frontend-monorepo")).toBeInTheDocument();
    expect(screen.getByText("acme-corp/backend-core")).toBeInTheDocument();

    // "Continue to Specifications" should be disabled when 0 selected
    const continueBtn = screen.getByRole("button", { name: /Continue to Specifications/i });
    expect(continueBtn).toBeDisabled();

    // Click "Select All Filtered"
    fireEvent.click(screen.getByText("Select All Filtered"));
    expect(screen.getByText(/2 Repositories Selected/i)).toBeInTheDocument();
    expect(continueBtn).toBeEnabled();

    // Deselect one by clicking its card
    fireEvent.click(screen.getByText("acme-corp/frontend-monorepo"));
    expect(screen.getByText(/1 Repositories Selected/i)).toBeInTheDocument();
    expect(continueBtn).toBeEnabled();
  });

  it("completes full 4-step wizard and submits multi-target pool configuration", async () => {
    const handleClose = vi.fn();
    render(
      <PoolWizardModal
        isOpen={true}
        onClose={handleClose}
        authProfiles={defaultAuthProfiles}
        hostOs="linux"
        hostArch="amd64"
      />,
    );

    // Step 1: Identity
    fireEvent.change(screen.getByLabelText(/Pool Name \(Slug\)/i), {
      target: { value: "multi-target-ci" },
    });
    fireEvent.click(screen.getByRole("button", { name: /Continue to Scope & Targets/i }));

    // Step 2: Select all targets
    fireEvent.click(screen.getByText("Select All Filtered"));
    fireEvent.click(screen.getByRole("button", { name: /Continue to Specifications/i }));

    // Step 3: Quotas & specs
    expect(screen.getByText("Runner Specs")).toBeInTheDocument();
    const labelsInput = screen.getByLabelText(/Runner Labels/i) as HTMLInputElement;
    expect(labelsInput.value).toBe("self-hosted,linux,amd64");

    fireEvent.change(screen.getByLabelText(/Min Idle Warm Runners/i), { target: { value: "2" } });
    fireEvent.change(screen.getByLabelText(/Max Concurrency/i), { target: { value: "8" } });
    fireEvent.click(screen.getByRole("button", { name: /Review & Confirm/i }));

    // Step 4: Review
    expect(screen.getByText("Review & Create")).toBeInTheDocument();
    expect(screen.getByText("multi-target-ci")).toBeInTheDocument();
    expect(screen.getByText("https://github.com/acme-corp/frontend-monorepo")).toBeInTheDocument();
    expect(screen.getByText("https://github.com/acme-corp/backend-core")).toBeInTheDocument();

    // Submit
    fireEvent.click(screen.getByRole("button", { name: /Create Runner Pool/i }));

    await waitFor(() => {
      expect(mockMutateAsync).toHaveBeenCalledTimes(1);
    });

    const submitted = mockMutateAsync.mock.calls[0][0].pool;
    expect(submitted.name).toBe("multi-target-ci");
    expect(submitted.provider).toBe("github");
    expect(submitted.scope).toBe("repo");
    expect(submitted.repositoryUrl).toBe("https://github.com/acme-corp/frontend-monorepo");
    expect(submitted.targetUrls).toEqual([
      "https://github.com/acme-corp/frontend-monorepo",
      "https://github.com/acme-corp/backend-core",
    ]);
    expect(submitted.minIdleRunners).toBe(2);
    expect(submitted.maxConcurrency).toBe(8);
    expect(handleClose).toHaveBeenCalledTimes(1);
  });

  it("locks docker-in-docker when provider is Forgejo or Gitea", () => {
    render(<PoolWizardModal isOpen={true} onClose={vi.fn()} authProfiles={defaultAuthProfiles} />);

    // Switch to internal-forgejo
    const profileSelect = screen.getByLabelText(/Git Authentication Profile/i);
    fireEvent.change(profileSelect, { target: { value: "20" } });
    expect(screen.getByText("forgejo")).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText(/Pool Name \(Slug\)/i), {
      target: { value: "forgejo-pool" },
    });
    fireEvent.click(screen.getByText("Continue to Scope & Targets"));

    fireEvent.click(screen.getByText("Select All Filtered"));
    fireEvent.click(screen.getByText("Continue to Specifications"));

    // Check Docker checkbox is disabled and locked
    const dockerCheckbox = screen.getByRole("checkbox", {
      name: /Enable Docker-in-Docker socket access/i,
    }) as HTMLInputElement;
    expect(dockerCheckbox.checked).toBe(true);
    expect(dockerCheckbox.disabled).toBe(true);
  });

  it("renders guided GitHub App installation callout when 0 targets discovered and installUrl is present", () => {
    mockDiscoveredTargets = [];
    mockInstallUrl = "https://github.com/apps/my-app/installations/new";

    render(<PoolWizardModal isOpen={true} onClose={vi.fn()} authProfiles={defaultAuthProfiles} />);

    fireEvent.change(screen.getByLabelText(/Pool Name \(Slug\)/i), {
      target: { value: "test-pool" },
    });
    fireEvent.click(screen.getByText("Continue to Scope & Targets"));

    expect(screen.getByText("GitHub App Not Installed Yet")).toBeInTheDocument();
    const installBtn = screen.getByRole("link", {
      name: /Install GitHub App on Your Account/i,
    });
    expect(installBtn).toBeInTheDocument();
    expect(installBtn).toHaveAttribute("href", "https://github.com/apps/my-app/installations/new");
    expect(installBtn).toHaveAttribute("target", "_blank");
    expect(installBtn).toHaveAttribute("rel", "noopener noreferrer");
  });

  it("renders 'Manage Access in GitHub' button linking to installation settings when targets are present", () => {
    mockInstallUrl = "https://github.com/apps/my-app/installations/new";
    mockInstallations = [
      {
        id: 12345n,
        accountLogin: "acme-corp",
        accountType: "Organization",
        htmlUrl: "https://github.com/organizations/acme-corp/settings/installations/12345",
        repositorySelection: "selected",
      },
    ];

    render(<PoolWizardModal isOpen={true} onClose={vi.fn()} authProfiles={defaultAuthProfiles} />);

    fireEvent.change(screen.getByLabelText(/Pool Name \(Slug\)/i), {
      target: { value: "test-pool" },
    });
    fireEvent.click(screen.getByText("Continue to Scope & Targets"));

    const manageBtn = screen.getByRole("link", {
      name: /Manage Access in GitHub/i,
    });
    expect(manageBtn).toBeInTheDocument();
    expect(manageBtn).toHaveAttribute(
      "href",
      "https://github.com/organizations/acme-corp/settings/installations/12345",
    );
    expect(manageBtn).toHaveAttribute("target", "_blank");
    expect(manageBtn).toHaveAttribute("rel", "noopener noreferrer");
  });
});

// A full pool fixture for edit-mode tests (docs/22 §7.2).
function makeEditPool(overrides: Record<string, unknown> = {}): Pool {
  return {
    id: 101n,
    name: "ci-edit-pool",
    provider: "github",
    repositoryUrl: "https://github.com/acme-corp/frontend-monorepo",
    scope: "repo",
    authProfileId: 10n,
    minIdleRunners: 1,
    maxConcurrency: 4,
    labels: ["self-hosted", "linux"],
    runnerImage: "ghcr.io/noosxe/runnero:v1",
    allowDocker: true,
    cpuLimit: "2",
    memoryLimit: "4GB",
    maxRunnerLifetimeSeconds: 3600,
    targetUrls: [
      "https://github.com/acme-corp/frontend-monorepo",
      "https://github.com/acme-corp/backend-core",
    ],
    renovate: undefined,
    activeRunners: 1,
    idleRunners: 2,
    ...overrides,
  } as unknown as Pool;
}

describe("PoolWizardModal (edit mode)", () => {
  const defaultAuthProfiles = [
    { id: 10n, name: "corp-github-app", authMethod: "github-app" },
    { id: 30n, name: "corp-github-pat", authMethod: "pat" },
    { id: 20n, name: "internal-forgejo", authMethod: "forgejo-token" },
  ];

  beforeEach(() => {
    vi.clearAllMocks();
    mockUpdateMutateAsync.mockResolvedValue({ pool: { id: 101n } });
  });

  it("prefills every step from the pool", () => {
    render(
      <PoolWizardModal
        isOpen={true}
        onClose={vi.fn()}
        mode="edit"
        pool={makeEditPool()}
        authProfiles={defaultAuthProfiles}
        hostOs="linux"
        hostArch="amd64"
      />,
    );

    expect(screen.getByText("Edit Runner Pool")).toBeInTheDocument();
    expect(screen.getByText("Review & Save")).toBeInTheDocument();

    const nameInput = screen.getByLabelText(/Pool Name \(Slug\)/i) as HTMLInputElement;
    expect(nameInput.value).toBe("ci-edit-pool");

    // Step 2: prefilled targets shown as removable chips
    fireEvent.click(screen.getByRole("button", { name: /Continue to Scope & Targets/i }));
    expect(screen.getByText("Selected Targets (2)")).toBeInTheDocument();
    expect(
      screen.getByLabelText("Remove target https://github.com/acme-corp/backend-core"),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Continue to Specifications/i })).toBeEnabled();

    // Step 3: prefilled quotas and labels
    fireEvent.click(screen.getByRole("button", { name: /Continue to Specifications/i }));
    expect((screen.getByLabelText(/Min Idle Warm Runners/i) as HTMLInputElement).value).toBe("1");
    expect((screen.getByLabelText(/Max Concurrency/i) as HTMLInputElement).value).toBe("4");
    expect((screen.getByLabelText(/Runner Labels/i) as HTMLInputElement).value).toBe(
      "self-hosted,linux",
    );
  });

  it("locks the auth profile selector to the pool's provider family", () => {
    render(
      <PoolWizardModal
        isOpen={true}
        onClose={vi.fn()}
        mode="edit"
        pool={makeEditPool({ provider: "forgejo", authProfileId: 20n })}
        authProfiles={defaultAuthProfiles}
      />,
    );

    const profileSelect = screen.getByLabelText(/Git Authentication Profile/i) as HTMLSelectElement;
    const options = Array.from(profileSelect.options).map((o) => o.value);
    expect(options).toEqual(["20"]); // only the forgejo profile is selectable
    expect(profileSelect.value).toBe("20");
    expect(
      screen.getByText(/Profile family is locked to the pool's forgejo provider/i),
    ).toBeInTheDocument();
  });

  it("shows a changed-fields diff and recycle banner on the review step", async () => {
    render(
      <PoolWizardModal
        isOpen={true}
        onClose={vi.fn()}
        mode="edit"
        pool={makeEditPool()}
        authProfiles={defaultAuthProfiles}
      />,
    );

    // Walk to step 3 and change labels (spawn identity)
    fireEvent.click(screen.getByRole("button", { name: /Continue to Scope & Targets/i }));
    fireEvent.click(screen.getByRole("button", { name: /Continue to Specifications/i }));
    fireEvent.change(screen.getByLabelText(/Runner Labels/i), {
      target: { value: "self-hosted,gpu" },
    });
    fireEvent.change(screen.getByLabelText(/Min Idle Warm Runners/i), { target: { value: "3" } });
    fireEvent.click(screen.getByRole("button", { name: /Review & Confirm/i }));

    expect(screen.getByText("Changed Fields (2)")).toBeInTheDocument();
    // Diff values are rendered as normalized (sorted) sets
    expect(screen.getByText(/linux, self-hosted/i)).toBeInTheDocument();
    expect(screen.getByText(/gpu, self-hosted/i)).toBeInTheDocument();
    expect(
      screen.getByText(/2 idle runners will be recycled to apply the new configuration/i),
    ).toBeInTheDocument();
  });

  it("shows the rename banner when the pool name changes", () => {
    render(
      <PoolWizardModal
        isOpen={true}
        onClose={vi.fn()}
        mode="edit"
        pool={makeEditPool()}
        authProfiles={defaultAuthProfiles}
      />,
    );

    fireEvent.change(screen.getByLabelText(/Pool Name \(Slug\)/i), {
      target: { value: "ci-edit-pool-renamed" },
    });
    fireEvent.click(screen.getByRole("button", { name: /Continue to Scope & Targets/i }));
    fireEvent.click(screen.getByRole("button", { name: /Continue to Specifications/i }));
    fireEvent.click(screen.getByRole("button", { name: /Review & Confirm/i }));

    expect(
      screen.getByText(/Renaming only changes how the pool is displayed/i),
    ).toBeInTheDocument();
    expect(screen.getByText(/runners are unaffected/i)).toBeInTheDocument();
  });

  it("submits the full pool with id and preserves max_runner_lifetime_seconds", async () => {
    const handleClose = vi.fn();
    render(
      <PoolWizardModal
        isOpen={true}
        onClose={handleClose}
        mode="edit"
        pool={makeEditPool()}
        authProfiles={defaultAuthProfiles}
      />,
    );

    fireEvent.click(screen.getByRole("button", { name: /Continue to Scope & Targets/i }));
    fireEvent.click(screen.getByRole("button", { name: /Continue to Specifications/i }));
    fireEvent.change(screen.getByLabelText(/Min Idle Warm Runners/i) as HTMLInputElement, {
      target: { value: "2" },
    });
    fireEvent.click(screen.getByRole("button", { name: /Review & Confirm/i }));
    fireEvent.click(screen.getByRole("button", { name: /Save Changes/i }));

    await waitFor(() => {
      expect(mockUpdateMutateAsync).toHaveBeenCalledTimes(1);
    });
    expect(mockMutateAsync).not.toHaveBeenCalled();

    const submitted = mockUpdateMutateAsync.mock.calls[0][0].pool;
    expect(submitted.id).toBe(101n);
    expect(submitted.name).toBe("ci-edit-pool");
    expect(submitted.provider).toBe("github");
    expect(submitted.authProfileId).toBe(10n);
    expect(submitted.scope).toBe("repo");
    expect(submitted.targetUrls).toEqual([
      "https://github.com/acme-corp/frontend-monorepo",
      "https://github.com/acme-corp/backend-core",
    ]);
    expect(submitted.labels).toEqual(["self-hosted", "linux"]);
    expect(submitted.minIdleRunners).toBe(2);
    expect(submitted.maxRunnerLifetimeSeconds).toBe(3600);
    expect(handleClose).toHaveBeenCalledTimes(1);
  });

  it("renders server rejection messages as banner text", async () => {
    mockUpdateMutateAsync.mockRejectedValue(
      new Error(
        '[failed_precondition] cannot rename pool "ci-edit-pool" to "ci-pool-2": 2 busy runner(s); wait for jobs to finish or terminate runners before renaming',
      ),
    );

    render(
      <PoolWizardModal
        isOpen={true}
        onClose={vi.fn()}
        mode="edit"
        pool={makeEditPool()}
        authProfiles={defaultAuthProfiles}
      />,
    );

    fireEvent.change(screen.getByLabelText(/Pool Name \(Slug\)/i), {
      target: { value: "ci-pool-2" },
    });
    fireEvent.click(screen.getByRole("button", { name: /Continue to Scope & Targets/i }));
    fireEvent.click(screen.getByRole("button", { name: /Continue to Specifications/i }));
    fireEvent.click(screen.getByRole("button", { name: /Review & Confirm/i }));
    fireEvent.click(screen.getByRole("button", { name: /Save Changes/i }));

    await waitFor(() => {
      expect(screen.getByText(/2 busy runner\(s\); wait for jobs to finish/i)).toBeInTheDocument();
    });
  });
});
