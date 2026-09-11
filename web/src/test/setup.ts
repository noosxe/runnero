import "@testing-library/jest-dom/vitest";
import { vi } from "vitest";

// Mock scrollTo which is not implemented in jsdom
Object.defineProperty(window, "scrollTo", { value: vi.fn(), writable: true });

// jsdom lacks matchMedia; needed by hooks/use-mobile (Sidebar mobile detection)
Object.defineProperty(window, "matchMedia", {
  writable: true,
  value: (query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addListener: vi.fn(),
    removeListener: vi.fn(),
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
    dispatchEvent: vi.fn(),
  }),
});

// jsdom lacks pointer-capture APIs; user-event's pointer implementation needs them
if (!window.HTMLElement.prototype.hasPointerCapture) {
  window.HTMLElement.prototype.hasPointerCapture = () => false;
  window.HTMLElement.prototype.setPointerCapture = () => {};
  window.HTMLElement.prototype.releasePointerCapture = () => {};
}
