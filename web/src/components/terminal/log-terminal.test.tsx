import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, act } from "@testing-library/react";
import { LogTerminal } from "./log-terminal";
import type { LogChunk } from "../../gen/api_pb";

const mockLogs: LogChunk[] = [
  {
    timestamp: "2026-09-04T00:50:01Z",
    stream: "stdout",
    content: "Connected to GitHub Actions API",
  } as LogChunk,
  {
    timestamp: "2026-09-04T00:50:02Z",
    stream: "stdout",
    content: "Listening for Jobs...",
  } as LogChunk,
  {
    timestamp: "2026-09-04T00:52:18Z",
    stream: "stderr",
    content: "Warning: Node.js 16 actions deprecated",
  } as LogChunk,
];

describe("LogTerminal", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("renders terminal header, status badge, and log chunks with stream labels", () => {
    render(
      <LogTerminal
        logs={mockLogs}
        mode="live"
        runnerName="runnero-arm64-prod-a8f12c"
        isConnected={true}
      />,
    );

    expect(screen.getByText("runnero-arm64-prod-a8f12c")).toBeInTheDocument();
    expect(screen.getByText("Live Stream")).toBeInTheDocument();
    expect(screen.getByText("Connected to GitHub Actions API")).toBeInTheDocument();
    expect(screen.getByText("Listening for Jobs...")).toBeInTheDocument();
    expect(screen.getByText("Warning: Node.js 16 actions deprecated")).toBeInTheDocument();
  });

  it("filters logs by stream type (stdout / stderr)", () => {
    render(
      <LogTerminal
        logs={mockLogs}
        mode="live"
        runnerName="runnero-arm64-prod-a8f12c"
        isConnected={true}
      />,
    );

    const stderrBtn = screen.getByRole("button", { name: "stderr" });
    fireEvent.click(stderrBtn);

    expect(screen.queryByText("Connected to GitHub Actions API")).not.toBeInTheDocument();
    expect(screen.getByText("Warning: Node.js 16 actions deprecated")).toBeInTheDocument();

    const stdoutBtn = screen.getByRole("button", { name: "stdout" });
    fireEvent.click(stdoutBtn);

    expect(screen.getByText("Connected to GitHub Actions API")).toBeInTheDocument();
    expect(screen.queryByText("Warning: Node.js 16 actions deprecated")).not.toBeInTheDocument();
  });

  it("filters logs by search query", () => {
    render(
      <LogTerminal logs={mockLogs} mode="historical" runnerName="runnero-arm64-prod-a8f12c" />,
    );

    const searchInput = screen.getByPlaceholderText("Filter log output...");
    fireEvent.change(searchInput, { target: { value: "deprecated" } });

    expect(screen.queryByText("Connected to GitHub Actions API")).not.toBeInTheDocument();
    expect(screen.getByText("Warning: Node.js 16 actions deprecated")).toBeInTheDocument();
  });

  it("toggles pause and resume in live mode", () => {
    render(
      <LogTerminal
        logs={mockLogs}
        mode="live"
        runnerName="runnero-arm64-prod-a8f12c"
        isConnected={true}
      />,
    );

    const pauseBtn = screen.getByRole("button", { name: /pause/i });
    fireEvent.click(pauseBtn);

    expect(screen.getByText("Stream Paused")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /resume/i })).toBeInTheDocument();
  });

  it("toggles auto-scroll", () => {
    render(<LogTerminal logs={mockLogs} mode="live" runnerName="runnero-arm64-prod-a8f12c" />);

    const autoScrollBtn = screen.getByRole("button", { name: /auto-scroll: on/i });
    fireEvent.click(autoScrollBtn);

    expect(screen.getByRole("button", { name: /auto-scroll: off/i })).toBeInTheDocument();
  });

  it("handles copy and export actions", async () => {
    const writeTextMock = vi.fn().mockResolvedValue(undefined);
    Object.assign(navigator, {
      clipboard: { writeText: writeTextMock },
    });

    const createObjectURLMock = vi.fn().mockReturnValue("blob:mock-url");
    window.URL.createObjectURL = createObjectURLMock;
    window.URL.revokeObjectURL = vi.fn();
    const clickSpy = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});

    render(
      <LogTerminal logs={mockLogs} mode="historical" runnerName="runnero-arm64-prod-a8f12c" />,
    );

    const copyBtn = screen.getByRole("button", { name: /copy/i });
    await act(async () => {
      fireEvent.click(copyBtn);
    });
    expect(writeTextMock).toHaveBeenCalled();

    const exportBtn = screen.getByRole("button", { name: /export/i });
    fireEvent.click(exportBtn);
    expect(createObjectURLMock).toHaveBeenCalled();
    expect(clickSpy).toHaveBeenCalled();

    clickSpy.mockRestore();
  });

  it("handles clear logs callback", () => {
    const clearMock = vi.fn();
    render(
      <LogTerminal
        logs={mockLogs}
        mode="live"
        runnerName="runnero-arm64-prod-a8f12c"
        onClear={clearMock}
      />,
    );

    const clearBtn = screen.getByRole("button", { name: /clear/i });
    fireEvent.click(clearBtn);

    expect(clearMock).toHaveBeenCalledTimes(1);
  });
});
