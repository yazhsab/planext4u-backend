import { initializeApp, getApps } from "firebase/app";
import { inMemoryPersistence, getAuth, getMultiFactorResolver, GoogleAuthProvider, setPersistence, signInWithEmailAndPassword, signInWithPopup, signOut, TotpMultiFactorGenerator, type MultiFactorError, type MultiFactorResolver, type UserCredential } from "firebase/auth";

export function operatorAuth() {
  const projectId = String(import.meta.env.VITE_FIREBASE_PROJECT_ID ?? "");
  const apiKey = String(import.meta.env.VITE_FIREBASE_API_KEY ?? "");
  const authDomain = String(import.meta.env.VITE_FIREBASE_AUTH_DOMAIN ?? "");
  if (!projectId || !apiKey || !authDomain) return null;
  const app = getApps().find((candidate) => candidate.name === "operator") ?? initializeApp({projectId, apiKey, authDomain}, "operator");
  return getAuth(app);
}

export type OperatorChallenge = {resolver: MultiFactorResolver; factorId: string};
export async function beginOperatorLogin(email: string, password: string, google = false): Promise<UserCredential | OperatorChallenge> {
  const auth = operatorAuth();
  if (!auth) throw new Error("Identity unavailable");
  // Firebase credentials live only for the sign-in handoff. The Go cookie owns
  // the administrator session; never persist the provider token in localStorage.
  await setPersistence(auth, inMemoryPersistence);
  await signOut(auth);
  try {
    return await (google ? signInWithPopup(auth, new GoogleAuthProvider()) : signInWithEmailAndPassword(auth, email.trim(), password));
  } catch (error) {
    if (typeof error === "object" && error !== null && "code" in error && error.code === "auth/multi-factor-auth-required") {
      const resolver = getMultiFactorResolver(auth, error as MultiFactorError);
      const factor = resolver.hints.find((hint) => hint.factorId === TotpMultiFactorGenerator.FACTOR_ID);
      if (factor) return {resolver, factorId: factor.uid};
    }
    throw error;
  }
}
export const completeOperatorChallenge = (challenge: OperatorChallenge, code: string) => challenge.resolver.resolveSignIn(TotpMultiFactorGenerator.assertionForSignIn(challenge.factorId, code.trim()));
export async function exchangeOperatorCredential(credential: UserCredential, country: string) {
  try {
    const token = await credential.user.getIdToken(true);
    const response = await fetch("/admin/api/v1/login", {method: "POST", credentials: "same-origin", headers: {"Content-Type": "application/json"}, body: JSON.stringify({id_token: token, country})});
    if (!response.ok) throw new Error("Administrator sign-in rejected");
  } finally {
    const auth = operatorAuth();
    if (auth) await signOut(auth);
  }
}
