import "@testing-library/jest-dom/vitest";
import { vi } from "vitest";

// Mock scrollTo which is not implemented in jsdom
Object.defineProperty(window, "scrollTo", { value: vi.fn(), writable: true });

// jsdom lacks pointer-capture APIs; user-event's pointer implementation needs them
if (!window.HTMLElement.prototype.hasPointerCapture) {
  window.HTMLElement.prototype.hasPointerCapture = () => false;
  window.HTMLElement.prototype.setPointerCapture = () => {};
  window.HTMLElement.prototype.releasePointerCapture = () => {};
}
