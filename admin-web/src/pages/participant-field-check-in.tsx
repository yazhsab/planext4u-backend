import {useEffect, useRef, useState} from "react";
import {APIError, type AdminSession} from "../api/client";
import {recordVendorFieldCheckIn, type Participant, type VendorFieldCheckIn} from "../api/participants";
import {Button} from "../components/button";
import {t} from "../lib/i18n";

type Location = {latitude: number; longitude: number; accuracy: number};
const hasGeolocation = (value: unknown): value is Geolocation => typeof value === "object" && value !== null && "getCurrentPosition" in value && typeof value.getCurrentPosition === "function";

export function ParticipantFieldCheckIn({session, participant, onRecorded, onDenied, onClose}: {session: AdminSession; participant: Participant; onRecorded: () => void; onDenied: () => void; onClose: () => void}) {
  const [location, setLocation] = useState<Location | null>(null);
  const [attempt, setAttempt] = useState<VendorFieldCheckIn | null>(null);
  const [status, setStatus] = useState<"idle" | "locating" | "saving">("idle");
  const [message, setMessage] = useState("");
  const [failed, setFailed] = useState(false);
  const mounted = useRef(false);
  const epoch = useRef(0);
  const busy = useRef(false);
  const request = useRef<AbortController | null>(null);
  useEffect(() => {mounted.current = true; return () => {mounted.current = false; epoch.current++; request.current?.abort();};}, []);
  const eligible = participant.kind === "VENDOR" && participant.status === "FIELD_VISIT_SCHEDULED" && participant.actions.includes("FIELD_CHECK_IN") && session.capabilities.includes("admin.participants.field_checkin");
  const fresh = session.assurance.fresh_auth && session.assurance.mfa_satisfied;
  const reset = () => {epoch.current++; request.current?.abort(); busy.current = false; setStatus("idle"); setLocation(null); setAttempt(null); setMessage(""); setFailed(false);};
  const capture = () => {
    if (busy.current || !eligible || !fresh || attempt) return;
    setLocation(null); setMessage(""); setFailed(false);
    const geo: unknown = Reflect.get(navigator, "geolocation");
    if (!hasGeolocation(geo)) {setMessage(t("fieldLocationUnavailable")); setFailed(true); return;}
    busy.current = true; setStatus("locating"); const generation = ++epoch.current;
    geo.getCurrentPosition((position) => {
      if (!mounted.current || generation !== epoch.current) return;
      busy.current = false; setStatus("idle");
      const {latitude, longitude, accuracy} = position.coords;
      if (![latitude, longitude, accuracy].every(Number.isFinite) || latitude < -90 || latitude > 90 || longitude < -180 || longitude > 180 || accuracy < 0) {setMessage(t("fieldLocationUnavailable")); setFailed(true); return;}
      setLocation({latitude, longitude, accuracy});
    }, (error) => {
      if (!mounted.current || generation !== epoch.current) return;
      busy.current = false; setStatus("idle"); setMessage(t(error.code === 1 ? "fieldPermissionDenied" : "fieldLocationUnavailable")); setFailed(true);
    }, {enableHighAccuracy: true, timeout: 10000, maximumAge: 0});
  };
  const submit = async () => {
    if (busy.current || !eligible || !fresh || !location) return;
    const command = attempt ?? {participant, point: {latitude: location.latitude, longitude: location.longitude}, key: crypto.randomUUID()};
    setAttempt(command); setStatus("saving"); setFailed(false); setMessage(""); busy.current = true;
    const generation = ++epoch.current; const controller = new AbortController(); request.current = controller;
    try {
      const receipt = await recordVendorFieldCheckIn(command, session.csrf_token, controller.signal);
      if (!mounted.current || generation !== epoch.current) return;
      setAttempt(null); setLocation(null); setMessage(`${t("participantFieldRecorded")}: ${receipt.status} (${String(receipt.revision)})`); onRecorded();
    } catch (error) {
      if (!mounted.current || generation !== epoch.current) return;
      setFailed(true);
      if (error instanceof APIError && error.status < 500) {setAttempt(null); setLocation(null); setMessage(t("participantFieldRefresh")); onDenied();}
      else {setMessage(t("participantFieldRetry"));}
    } finally {
      if (mounted.current && generation === epoch.current) {busy.current = false; setStatus("idle"); request.current = null;}
    }
  };
  if (!eligible) return null;
  return <section className="surface-card field-check-in" aria-label={t("participantFieldTitle")}>
    <h2>{t("participantFieldTitle")}: {participant.display_name}</h2><p>{t("participantFieldHelp")}</p>
    {!fresh && <p role="alert">{t("participantFresh")}</p>}
    <div className="field-actions">
      <Button type="button" disabled={!fresh || status !== "idle" || attempt !== null} onClick={capture}>{status === "locating" ? t("fieldLocating") : t("fieldCapture")}</Button>
      {location && <><p>{t("fieldAccuracy")} {Math.ceil(location.accuracy)} m</p><Button type="button" disabled={!fresh || status !== "idle"} onClick={() => {void submit();}}>{status === "saving" ? t("fieldSaving") : attempt ? t("fieldRetryButton") : t("fieldConfirm")}</Button></>}
      {(location !== null || attempt !== null || status === "locating") && <Button type="button" disabled={status === "saving"} onClick={reset}>{t("fieldReset")}</Button>}
      <Button type="button" disabled={status === "saving"} onClick={onClose}>{t("cancelEdit")}</Button>
    </div>
    {message && <p role={failed ? "alert" : "status"}>{message}</p>}
  </section>;
}
