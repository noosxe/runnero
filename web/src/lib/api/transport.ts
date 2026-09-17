import {
  Code,
  ConnectError,
  createClient,
  type Interceptor,
  type Transport,
} from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import {
  AuthService,
  UserService,
  PoolService,
  AuthProfileService,
  OnboardingService,
  AnalyticsService,
  LogService,
  RenovateService,
  ImageUpdateService,
} from "../../gen/api_pb";

export interface TransportOptions {
  baseUrl?: string;
  onUnauthenticated?: () => void;
}

// Event fired on every PermissionDenied response (RUN-236, docs/35 §2.4):
// unlike Unauthenticated this must NOT route to login - the session is
// valid, the role is not enough. The mounted PermissionDeniedToast listener
// renders the feedback; the caller stays logged in.
export const PERMISSION_DENIED_EVENT = "runnero:permission-denied";

export function createAuthInterceptor(onUnauthenticated?: () => void): Interceptor {
  return (next) => async (req) => {
    try {
      return await next(req);
    } catch (err) {
      if (err instanceof ConnectError && err.code === Code.Unauthenticated) {
        if (onUnauthenticated) {
          onUnauthenticated();
        } else if (
          typeof window !== "undefined" &&
          !window.location.pathname.startsWith("/login") &&
          !window.location.pathname.startsWith("/onboarding")
        ) {
          const redirect = encodeURIComponent(window.location.pathname);
          window.location.href = `/login?redirect=${redirect}`;
        }
      } else if (err instanceof ConnectError && err.code === Code.PermissionDenied) {
        // Mid-session demotion path (docs/35 §2.4): toast, stay logged in.
        if (typeof window !== "undefined") {
          window.dispatchEvent(new CustomEvent(PERMISSION_DENIED_EVENT));
        }
      }
      throw err;
    }
  };
}

export function createSupervisorTransport(options: TransportOptions = {}): Transport {
  const baseUrl =
    options.baseUrl ??
    (typeof window !== "undefined" ? window.location.origin : "http://localhost:8080");

  return createConnectTransport({
    baseUrl,
    useBinaryFormat: true,
    interceptors: [createAuthInterceptor(options.onUnauthenticated)],
    fetch: (input, init) =>
      fetch(input, {
        ...init,
        credentials: "same-origin",
      }),
  });
}

// Default singleton transport for browser application
export const defaultTransport = createSupervisorTransport();

// Service clients bound to default transport
export const authClient = createClient(AuthService, defaultTransport);
export const userClient = createClient(UserService, defaultTransport);
export const poolClient = createClient(PoolService, defaultTransport);
export const authProfileClient = createClient(AuthProfileService, defaultTransport);
export const onboardingClient = createClient(OnboardingService, defaultTransport);
export const analyticsClient = createClient(AnalyticsService, defaultTransport);
export const logClient = createClient(LogService, defaultTransport);
export const renovateClient = createClient(RenovateService, defaultTransport);
export const imageClient = createClient(ImageUpdateService, defaultTransport);
