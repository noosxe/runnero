import { describe, expect, it, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { createRouterMock } from "./router-mock";

describe("createRouterMock", () => {
  it("exposes the full router surface route components consume", () => {
    const mock = createRouterMock();
    // RUN-183: no suite may crash on a missing export — these five keys are
    // the union of every router import in src today plus future-proofing.
    expect(Object.keys(mock)).toEqual(
      expect.arrayContaining(["Link", "createLink", "useNavigate", "useParams", "useSearch"]),
    );
  });

  it("renders Link as an anchor with to as href and swallows navigation", () => {
    const onClick = vi.fn();
    const { Link: StubLink } = createRouterMock();
    render(
      // the mock's Link is a loose stand-in, not the real router type
      <StubLink to="/profiles" onClick={onClick} data-testid="stub">
        Go
      </StubLink>,
    );

    const anchor = screen.getByTestId("stub");
    expect(anchor).toHaveAttribute("href", "/profiles");
    expect(anchor).toHaveTextContent("Go");

    fireEvent.click(anchor);
    expect(onClick).toHaveBeenCalledOnce();
  });

  it("createLink composes identity-wise, so host components render", () => {
    const mock = createRouterMock();
    const Host = ({ label }: { label: string }) => <span>{label}</span>;
    const Linked = mock.createLink(Host);
    render(<Linked label="composed" />);
    expect(screen.getByText("composed")).toBeInTheDocument();
  });

  it("hook defaults are inert callables; overrides win", () => {
    const navigate = vi.fn();
    const mock = createRouterMock({ useNavigate: () => navigate });

    expect(mock.useNavigate()).toBe(navigate);
    expect(mock.useParams()).toEqual({});
    expect(mock.useSearch()).toEqual({});

    const plain = createRouterMock();
    expect(vi.isMockFunction(plain.useNavigate())).toBe(true);
  });
});
