import { useState, type SyntheticEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useOutletContext } from "react-router";
import type { AdminSession } from "../api/client";
import { getMasterHistory, getMasters, masterKinds, masterParents, saveMaster, type Master, type MasterChange, type MasterKind } from "../api/masters";
import { Button } from "../components/button";
import { t } from "../lib/i18n";

const labels = () => ({COUNTRY: t("masterCountry"), STATE: t("masterState"), DISTRICT: t("masterDistrict"), CITY: t("masterCity"), AREA: t("masterArea"), OCCUPATION: t("masterOccupation"), REASON: t("masterReason")});
const formText = (form: FormData, name: string) => {const value = form.get(name); return typeof value === "string" ? value.trim() : "";};
export function MastersPage() {
  const session = useOutletContext<AdminSession>();
  const [kind, setKind] = useState<MasterKind>("OCCUPATION");
  if (!session.roles.some((role) => ["SUPER_ADMIN", "COUNTRY_ADMIN", "AUDITOR"].includes(role))) return <p role="alert">{t("mastersDenied")}</p>;
  return <div className="page-stack operator-workspace master-workspace"><header className="page-header"><div><h1>{t("mastersTitle")}</h1><p>{t("mastersHelp")}</p></div></header>
    <label>{t("masterType")}<select value={kind} onChange={(event) => {setKind(event.target.value as MasterKind);}}>{masterKinds.map((value) => <option key={value} value={value}>{labels()[value]}</option>)}</select></label>
    <MasterWorkspace key={`${session.subject_id}:${session.selected_country}:${kind}`} session={session} kind={kind} />
  </div>;
}
function MasterWorkspace({session, kind}: {session: AdminSession; kind: MasterKind}) {
  const client = useQueryClient();
  const [search, setSearch] = useState(""); const [status, setStatus] = useState("ALL"); const [after, setAfter] = useState("");
  const [editing, setEditing] = useState<Master | null>(null); const [version, setVersion] = useState(0); const [saved, setSaved] = useState(false);
  const [parentSearch, setParentSearch] = useState(""); const [parentAfter, setParentAfter] = useState(""); const [parentID, setParentID] = useState("");
  const [history, setHistory] = useState<Master | null>(null); const [before, setBefore] = useState(0);
  const scope = ["admin", "masters", session.subject_id, session.selected_country];
  const query = useQuery({queryKey: [...scope, kind, search, status, after], queryFn: ({signal}) => getMasters(kind, search, status, after, signal), retry: false});
  const parentKind = masterParents[kind];
  const parents = useQuery({queryKey: [...scope, "parents", parentKind, parentSearch, parentAfter], queryFn: ({signal}) => getMasters(parentKind ?? "COUNTRY", parentSearch, "ACTIVE", parentAfter, signal), enabled: parentKind !== undefined && editing === null, retry: false});
  const events = useQuery({queryKey: [...scope, "history", history?.id, before], queryFn: ({signal}) => getMasterHistory(history?.id ?? "", before, signal), enabled: history !== null, retry: false});
  const reset = () => {setEditing(null); setParentID(""); setVersion((value) => value + 1);};
  const mutation = useMutation({mutationFn: (value: MasterChange) => saveMaster(value, session.csrf_token), onSuccess: async () => {reset(); setSaved(true); await client.invalidateQueries({queryKey: scope});}});
  const manage = session.roles.some((role) => role === "SUPER_ADMIN" || role === "COUNTRY_ADMIN");
  const busy = mutation.isPending || !session.assurance.fresh_auth || !session.assurance.mfa_satisfied;
  const submit = (event: SyntheticEvent<HTMLFormElement>) => {
    event.preventDefault(); const form = new FormData(event.currentTarget); setSaved(false);
    mutation.mutate({id: editing?.id ?? crypto.randomUUID(), kind, code: editing?.code ?? formText(form, "code"), name_en: formText(form, "name_en"), name_ta: formText(form, "name_ta"), active: formText(form, "active") === "true", revision: editing?.revision ?? 0, reason: formText(form, "reason"), ...(parentKind ? {parent_id: editing?.parent_id ?? parentID} : {})});
  };
  return <>
    <form className="surface-card" onSubmit={(event) => {event.preventDefault(); const form = new FormData(event.currentTarget); setSearch(formText(form, "search")); setStatus(formText(form, "status")); setAfter("");}}>
      <label>{t("masterSearch")}<input name="search" maxLength={128} /></label><label>{t("status")}<select name="status"><option value="ALL">{t("masterAll")}</option><option value="ACTIVE">{t("masterActive")}</option><option value="INACTIVE">{t("masterInactive")}</option></select></label><Button>{t("masterFind")}</Button>
      <Button type="button" onClick={() => {reset(); mutation.reset(); setSaved(false); void query.refetch();}} disabled={mutation.isPending}>{t("refreshDirectory")}</Button>
    </form>
    {query.isPending && <p role="status">{t("mastersLoading")}</p>}
    {query.isError && <p role="alert">{query.error.message}<Button type="button" onClick={() => {void query.refetch();}}>{t("retry")}</Button></p>}
    {query.data && <section className="surface-card"><h2>{labels()[kind]}</h2>{query.data.items.length === 0 && <p>{t("mastersEmpty")}</p>}
      <ul className="operator-list">{query.data.items.map((item) => <li key={item.id}><strong>{navigator.language.startsWith("ta") ? item.name_ta : item.name_en}</strong><p>{item.code} · {t(item.active ? "masterActive" : "masterInactive")}</p>
        {manage && <Button type="button" disabled={busy} onClick={() => {setEditing(item); setParentID(item.parent_id ?? ""); setVersion((value) => value + 1); setSaved(false); mutation.reset();}}>{t("masterEdit")}: {item.name_en}</Button>}
        <Button type="button" onClick={() => {setHistory(item); setBefore(0);}}>{t("masterHistory")}: {item.name_en}</Button></li>)}</ul>
      {after && <Button type="button" onClick={() => {setAfter("");}}>{t("firstPage")}</Button>}{query.data.next_after && <Button type="button" onClick={() => {setAfter(query.data.next_after ?? "");}}>{t("nextPage")}</Button>}
    </section>}
    {manage && <section className="surface-card"><h2>{t(editing ? "masterEdit" : "masterCreate")}</h2>
      {!session.assurance.fresh_auth && <p role="status">{t("masterFresh")}</p>}{saved && <p role="status">{t("masterSaved")}</p>}{mutation.isError && <p role="alert">{mutation.error.message}</p>}
      {parentKind && !editing && <div><label>{t("masterParentSearch")}<input value={parentSearch} maxLength={128} onChange={(event) => {setParentSearch(event.target.value); setParentAfter(""); setParentID("");}} /></label>
        {parents.isPending && <p role="status">{t("mastersLoading")}</p>}{parents.isError && <p role="alert">{parents.error.message}</p>}
        {parentAfter && <Button type="button" onClick={() => {setParentAfter(""); setParentID("");}}>{t("masterParentFirst")}</Button>}{parents.data?.next_after && <Button type="button" onClick={() => {setParentAfter(parents.data.next_after ?? ""); setParentID("");}}>{t("masterParentNext")}</Button>}
      </div>}
      <form key={version} onSubmit={submit}><fieldset disabled={busy}><legend>{labels()[kind]}</legend>
        <label>{t("masterCode")}<input name="code" defaultValue={editing?.code ?? (kind === "COUNTRY" ? session.selected_country : "")} readOnly={editing !== null || kind === "COUNTRY"} pattern="[A-Z][A-Z0-9_]{0,63}" maxLength={64} required /></label>
        {parentKind && <label>{t("masterParent")} {labels()[parentKind]}{editing ? <input value={editing.parent_id} readOnly /> : <select value={parentID} required onChange={(event) => {setParentID(event.target.value);}}><option value="">{t("select")}</option>{parents.data?.items.map((item) => <option key={item.id} value={item.id}>{item.name_en} · {item.code}</option>)}</select>}</label>}
        <label>{t("masterEnglish")}<input name="name_en" defaultValue={editing?.name_en ?? ""} maxLength={128} required /></label><label>{t("masterTamil")}<input name="name_ta" lang="ta" defaultValue={editing?.name_ta ?? ""} maxLength={128} required /></label>
        <label>{t("masterEntryStatus")}<select name="active" defaultValue={String(editing?.active ?? true)}><option value="true">{t("masterActive")}</option><option value="false">{t("masterInactive")}</option></select></label>
        <label>{t("masterChangeReason")}<textarea name="reason" minLength={8} maxLength={500} required /></label><p>{t("masterStable")}</p><Button>{t("masterSave")}</Button><Button type="button" onClick={() => {reset(); mutation.reset(); setSaved(false);}}>{t("cancelEdit")}</Button>
      </fieldset></form>
    </section>}
    {history && <section className="surface-card"><h2>{t("masterHistory")}: {history.name_en}</h2>{events.isPending && <p role="status">{t("mastersLoading")}</p>}{events.isError && <p role="alert">{events.error.message}</p>}
      {events.data && <><ol className="master-history">{events.data.items.map((item) => <li key={item.definition.revision}><strong>{item.definition.name_en} / {item.definition.name_ta}</strong><p>{item.reason}</p><small>{item.actor_id} · {new Date(item.occurred_at).toLocaleString()} · {t(item.definition.active ? "masterActive" : "masterInactive")}</small></li>)}</ol>{events.data.items.length === 0 && <p>{t("noOperatorHistory")}</p>}{events.data.next_before && <Button type="button" onClick={() => {setBefore(events.data.next_before ?? 0);}}>{t("olderHistory")}</Button>}{before > 0 && <Button type="button" onClick={() => {setBefore(0);}}>{t("latestHistory")}</Button>}</>}
      <Button type="button" onClick={() => {setHistory(null);}}>{t("closeHistory")}</Button>
    </section>}
  </>;
}
