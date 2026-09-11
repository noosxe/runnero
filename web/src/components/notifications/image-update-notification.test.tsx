import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent, act } from "@testing-library/react";
import { ImageUpdateNotification } from "./image-update-notification";
import type { ImageUpdate } from "../../gen/api_pb";

const mockUpdates: ImageUpdate[] = [
  {
    id: 10n,
    poolId: 1n,
    currentImage: "ghcr.io/noosxe/runnero:v1.1.0",
    latestDigest: "ghcr.io/noosxe/runnero:v1.2.0",
    status: "available",
    checkedAt: "2026-09-04T00:00:00Z",
  } as ImageUpdate,
];

const mockPullMutate = vi.fn();
const mockDismissMutate = vi.fn();

vi.mock("../../lib/api/query-hooks", () => ({
  usePullImage: () => ({
    mutateAsync: mockPullMutate,
  }),
  useDismissImageUpdate: () => ({
    mutateAsync: mockDismissMutate,
  }),
}));

describe("ImageUpdateNotification", () => {
  it("renders nothing when updates array is empty", () => {
    const { container } = render(<ImageUpdateNotification updates={[]} />);
    expect(container).toBeEmptyDOMElement();
  });

  it("renders update notification card and handles pull / dismiss clicks", async () => {
    render(
      <ImageUpdateNotification updates={mockUpdates} poolNameLookup={{ "1": "pool-linux-ci" }} />,
    );

    expect(screen.getByText("Runner Image Update Available")).toBeInTheDocument();
    expect(screen.getByText("pool-linux-ci")).toBeInTheDocument();
    expect(screen.getByText("ghcr.io/noosxe/runnero:v1.1.0")).toBeInTheDocument();
    expect(screen.getByText("ghcr.io/noosxe/runnero:v1.2.0")).toBeInTheDocument();

    const pullBtn = screen.getByRole("button", { name: /pull update/i });
    await act(async () => {
      fireEvent.click(pullBtn);
    });
    expect(mockPullMutate).toHaveBeenCalledWith(1n);

    const dismissBtn = screen.getByRole("button", { name: "Dismiss update notification" });
    await act(async () => {
      fireEvent.click(dismissBtn);
    });
    expect(mockDismissMutate).toHaveBeenCalledWith(10n);
  });
});
