import {useEffect,useRef,useState} from "react";
import {APIError,type AdminSession} from "../api/client";
import {recordFieldVisit} from "../api/fulfillment";
import {Button} from "../components/button";
import {t} from "../lib/i18n";

type Location = {latitude: number;longitude: number;accuracy: number};
type Attempt = {territoryID: string;point: {latitude: number;longitude: number};key: string};
function hasGeolocation(value: unknown): value is Geolocation {
  return typeof value==="object" && value!==null && "getCurrentPosition" in value && typeof value.getCurrentPosition==="function";
}

export function FieldCheckIn({session,territories,onRecorded,onDenied}: {session: AdminSession;territories: {id: string;name: string}[];onRecorded: () => void;onDenied: () => void}) {
  const [territory,setTerritory] = useState(territories[0]?.id ?? "");
  const [location,setLocation] = useState<Location | null>(null);
  const [attempt,setAttempt] = useState<Attempt | null>(null);
  const [status,setStatus] = useState<"idle" | "locating" | "saving">("idle");
  const [message,setMessage] = useState("");
  const [failed,setFailed] = useState(false);
  const mounted = useRef(false);
  const epoch = useRef(0);
  const busy = useRef(false);
  const request = useRef<AbortController | null>(null);
  useEffect(() => {mounted.current=true;return () => {mounted.current=false;epoch.current++;request.current?.abort();};},[]);
  const selected = territories.some((item) => item.id===territory) ? territory : territories[0]?.id ?? "";
  const reset = () => {epoch.current++;setLocation(null);setAttempt(null);setMessage("");setFailed(false);setStatus("idle");busy.current=false;};
  const capture = () => {
    if (busy.current || !session.assurance.fresh_auth || !selected) return;
    setLocation(null);setAttempt(null);setMessage("");setFailed(false);
    const geolocation: unknown = Reflect.get(navigator,"geolocation");
    if (!hasGeolocation(geolocation)) {setFailed(true);setMessage(t("fieldLocationUnavailable"));return;}
    busy.current=true;setStatus("locating");
    const generation=++epoch.current;
    geolocation.getCurrentPosition((position) => {
      if (!mounted.current || epoch.current!==generation) return;
      busy.current=false;setStatus("idle");
      const {latitude,longitude,accuracy}=position.coords;
      if (![latitude,longitude,accuracy].every(Number.isFinite) || latitude < -90 || latitude > 90 || longitude < -180 || longitude > 180 || accuracy < 0) {setFailed(true);setMessage(t("fieldLocationUnavailable"));return;}
      setLocation({latitude,longitude,accuracy});
    },(error) => {
      if (!mounted.current || epoch.current!==generation) return;
      busy.current=false;setStatus("idle");setFailed(true);setMessage(t(error.code===1 ? "fieldPermissionDenied" : "fieldLocationUnavailable"));
    },{enableHighAccuracy:true,timeout:10000,maximumAge:0});
  };
  const submit = async () => {
    if (busy.current || !location || !session.assurance.fresh_auth || !selected) return;
    const current=attempt ?? {territoryID:selected,point:{latitude:location.latitude,longitude:location.longitude},key:crypto.randomUUID()};
    setAttempt(current);setFailed(false);setMessage("");setStatus("saving");busy.current=true;
    const generation=++epoch.current;const controller=new AbortController();request.current=controller;
    try {
      await recordFieldVisit(current.territoryID,current.point,current.key,session.csrf_token,controller.signal);
      if (!mounted.current || epoch.current!==generation) return;
      setLocation(null);setAttempt(null);setMessage(t("fieldRecorded"));onRecorded();
    } catch (error) {
      if (!mounted.current || epoch.current!==generation) return;
      setFailed(true);
      if (error instanceof APIError && (error.status===401 || error.status===403 || error.status===404)) {
        setLocation(null);setAttempt(null);setMessage(t(error.status===401 ? "fieldReauth" : "fieldNotAllowed"));onDenied();
      } else {setMessage(t("fieldRetry"));}
    } finally {
      if (mounted.current && epoch.current===generation) {busy.current=false;setStatus("idle");}
    }
  };
  if (!selected) return null;
  return <section className="field-check-in" aria-label={t("fieldTitle")}>
    <h3>{t("fieldTitle")}</h3><p>{t("fieldHelp")}</p>
    {!session.assurance.fresh_auth && <p role="status"><a href="/login?reauth=mfa">{t("fieldReauth")}</a></p>}
    <label htmlFor="field-territory">{t("fieldTerritory")}</label>
    <select id="field-territory" value={selected} disabled={status!=="idle" || attempt!==null} onChange={(event) => {reset();setTerritory(event.target.value);}}>{territories.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</select>
    <div className="field-actions"><Button disabled={status!=="idle" || !session.assurance.fresh_auth || attempt!==null} onClick={capture}>{status==="locating" ? t("fieldLocating") : t("fieldCapture")}</Button>
      {location && <><p>{t("fieldAccuracy")} {Math.ceil(location.accuracy)} m</p><Button disabled={status!=="idle" || !session.assurance.fresh_auth} onClick={() => {void submit();}}>{status==="saving" ? t("fieldSaving") : attempt ? t("fieldRetryButton") : t("fieldConfirm")}</Button></>}
      {(location!==null || attempt!==null || status==="locating") && <Button variant="secondary" disabled={status==="saving"} onClick={reset}>{t("fieldReset")}</Button>}
    </div>
    {message && <p role={failed ? "alert" : "status"}>{message}</p>}
  </section>;
}
