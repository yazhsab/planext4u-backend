import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { beginOperatorLogin, completeOperatorChallenge, exchangeOperatorCredential, operatorAuth } from "./firebase-login";

const mocks = vi.hoisted(() => ({
  apps: vi.fn(() => []), init: vi.fn(() => ({name: "operator"})), auth: vi.fn(() => ({})),
  persistence: vi.fn(), signOut: vi.fn(), email: vi.fn(), google: vi.fn(),
  token: vi.fn(() => Promise.resolve("verified-provider-token")), resolver: vi.fn(), assertion: vi.fn(), resolve: vi.fn(),
}));
vi.mock("firebase/app", () => ({getApps: mocks.apps, initializeApp: mocks.init}));
vi.mock("firebase/auth", () => ({
  inMemoryPersistence: "memory", getAuth: mocks.auth, setPersistence: mocks.persistence, signOut: mocks.signOut,
  signInWithEmailAndPassword: mocks.email, signInWithPopup: mocks.google, GoogleAuthProvider: class {readonly providerId = "google.com";},
  getMultiFactorResolver: mocks.resolver,
  TotpMultiFactorGenerator: {FACTOR_ID: "totp", assertionForSignIn: mocks.assertion},
}));

afterEach(() => { vi.unstubAllEnvs(); vi.unstubAllGlobals(); vi.clearAllMocks(); });
beforeEach(() => {
  vi.stubEnv("VITE_FIREBASE_PROJECT_ID", "test-project"); vi.stubEnv("VITE_FIREBASE_API_KEY", "public-client-key"); vi.stubEnv("VITE_FIREBASE_AUTH_DOMAIN", "test-project.firebaseapp.com");
  mocks.email.mockResolvedValue({user: {getIdToken: mocks.token}});
  mocks.google.mockResolvedValue({user: {getIdToken: mocks.token}});
});

describe("operator provider handoff", () => {
  it("uses memory persistence and exchanges only verified evidence for a cookie", async () => {
    const fetch = vi.fn<typeof globalThis.fetch>(); fetch.mockResolvedValue(new Response(null, {status: 201})); vi.stubGlobal("fetch", fetch);
    const credential = await beginOperatorLogin(" operator@example.test ", "synthetic-secret");
    if ("resolver" in credential) throw new Error("Unexpected challenge");
    await exchangeOperatorCredential(credential, "IN");
    expect(mocks.persistence).toHaveBeenCalledWith(expect.anything(), "memory");
    expect(mocks.email).toHaveBeenCalledWith(expect.anything(), "operator@example.test", "synthetic-secret");
    const init = fetch.mock.calls[0]?.[1];
    if (!init || typeof init.body !== "string") throw new Error("Missing credential exchange");
    expect(init.credentials).toBe("same-origin");
    expect(JSON.parse(init.body)).toEqual({id_token: "verified-provider-token", country: "IN"});
    expect(mocks.signOut).toHaveBeenCalledTimes(2);
  });
  it("requires the enrolled TOTP factor and resolves it through the provider", async () => {
    mocks.email.mockRejectedValueOnce({code: "auth/multi-factor-auth-required"});
    mocks.resolver.mockReturnValue({hints: [{factorId: "phone", uid: "phone-factor"}, {factorId: "totp", uid: "totp-factor"}], resolveSignIn: mocks.resolve});
    mocks.assertion.mockReturnValue("provider-assertion");
    const challenge = await beginOperatorLogin("operator@example.test", "synthetic-secret");
    if (!("resolver" in challenge)) throw new Error("Missing challenge");
    await completeOperatorChallenge(challenge, " 123456 ");
    expect(mocks.assertion).toHaveBeenCalledWith("totp-factor", "123456");
    expect(mocks.resolve).toHaveBeenCalledWith("provider-assertion");
  });
  it("clears the provider session even when server-owned access is rejected", async () => {
    vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(new Response(null, {status: 403}))));
    const credential = await beginOperatorLogin("", "", true);
    if ("resolver" in credential) throw new Error("Unexpected challenge");
    await expect(exchangeOperatorCredential(credential, "NG")).rejects.toThrow("rejected");
    expect(mocks.signOut).toHaveBeenCalledTimes(2);
    vi.stubEnv("VITE_FIREBASE_PROJECT_ID", "");
    expect(operatorAuth()).toBeNull();
    await expect(beginOperatorLogin("", "")).rejects.toThrow("unavailable");
  });
});
