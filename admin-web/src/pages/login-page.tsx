import { Button } from "../components/button";
import { useState, type SyntheticEvent } from "react";
import { beginOperatorLogin, completeOperatorChallenge, exchangeOperatorCredential, operatorAuth, type OperatorChallenge } from "../lib/firebase-login";
import { t } from "../lib/i18n";

export function LoginPage() {
  const [challenge, setChallenge] = useState<OperatorChallenge | null>(null);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  const [country, setCountry] = useState("IN");
  const configured = operatorAuth() !== null;
  const login = async (event: SyntheticEvent<HTMLFormElement>) => {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    const text = (key: string) => {const value = form.get(key); return typeof value === "string" ? value : "";};
    setBusy(true); setMessage("");
    try {
      const result = challenge ? await completeOperatorChallenge(challenge, text("code")) : await beginOperatorLogin(text("email"), text("password"));
      if ("resolver" in result) { setChallenge(result); return; }
      await exchangeOperatorCredential(result, country);
      window.location.assign("/");
    } catch { setMessage(t("authFailure")); }
    finally { setBusy(false); }
  };
  return <main className="session-gate operator-login"><section className="gate-card"><h1>{t("signin")}</h1>
    {!configured && <p role="status">{t("authUnavailable")}</p>}
    {message && <p role="alert">{message}</p>}
    <form onSubmit={(event) => { void login(event); }}>
      <label>{t("country")}<input value={country} onChange={(event) => { setCountry(event.target.value.toUpperCase()); }} required pattern="[A-Z]{2}" maxLength={2} /></label>
      {challenge ? <label>{t("verification")}<input name="code" autoComplete="one-time-code" inputMode="numeric" required pattern="[0-9]{6}" /></label> : <><label>{t("email")}<input name="email" type="email" autoComplete="username" required /></label><label>{t("password")}<input name="password" type="password" autoComplete="current-password" required /></label></>}
      <Button disabled={!configured || busy}>{challenge ? t("verify") : t("signin")}</Button>
    </form>
  </section></main>;
}
