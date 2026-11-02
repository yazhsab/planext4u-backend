import { useState, type SyntheticEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useOutletContext } from "react-router";
import { AlertTriangle } from "lucide-react";
import type { AdminSession } from "../api/client";
import { createOperator, getOperatorEvents, getOperators, operatorRoles, updateOperator, type OperatorAccount, type OperatorChange } from "../api/operators";
import { Button } from "../components/button";
import { StatePanel } from "../components/state-panel";
import { t } from "../lib/i18n";

export function OperatorsPage() {
  const session = useOutletContext<AdminSession>();
  if (!session.roles.includes("SUPER_ADMIN")) return <p role="alert">{t("operatorsDenied")}</p>;
  return <OperatorsWorkspace key={`${session.subject_id}:${session.selected_country}`} session={session} />;
}
const roleLabel = (role: string) => ({COUNTRY_ADMIN: t("countryAdmin"), FINANCE: t("financeAdmin"), CONTENT_ADMIN: t("contentAdmin"), SUPPORT_ADMIN: t("supportAdmin"), AUDITOR: t("auditor"), REGIONAL_STAFF: t("regionalStaff"), SUPER_ADMIN: t("superAdmin")})[role] ?? role;
const formText = (form: FormData, key: string) => {const value = form.get(key); return typeof value === "string" ? value.trim() : "";};

function OperatorsWorkspace({session}: {session: AdminSession}) {
  const client = useQueryClient();
  const [search, setSearch] = useState("");
  const [after, setAfter] = useState("");
  const [editing, setEditing] = useState<OperatorAccount | null>(null);
  const [history, setHistory] = useState<OperatorAccount | null>(null);
  const [before, setBefore] = useState(0);
  const [formVersion, setFormVersion] = useState(0);
  const [saved, setSaved] = useState(false);
  const scope = ["admin", "operators", session.subject_id, session.selected_country];
  const query = useQuery({queryKey: [...scope, search, after], queryFn: ({signal}) => getOperators(search, after, signal), retry: false});
  const events = useQuery({queryKey: [...scope, "history", history?.subject_id, before], queryFn: ({signal}) => getOperatorEvents(history?.subject_id ?? "", before, signal), enabled: history !== null, retry: false});
  const mutation = useMutation({mutationFn: (value: {id?: string; input: OperatorChange; providerSubject: string}) => value.id ? updateOperator(value.id, value.input, session.csrf_token) : createOperator({...value.input, provider_subject: value.providerSubject}, session.csrf_token), onSuccess: async () => { setEditing(null); setFormVersion((value) => value + 1); setSaved(true); await client.invalidateQueries({queryKey: scope}); }});
  const busy = mutation.isPending || !session.assurance.fresh_auth || !session.assurance.mfa_satisfied;
  const reset = () => {setEditing(null); setFormVersion((value) => value + 1); mutation.reset(); setSaved(false);};
  const submit = (event: SyntheticEvent<HTMLFormElement>) => {
    event.preventDefault(); const form = new FormData(event.currentTarget);
    const text = (key: string) => formText(form, key);
    const input: OperatorChange = {display_name: text("display_name"), roles: operatorRoles.filter((role) => form.getAll("role").includes(role)), allowed_countries: [session.selected_country, ...session.allowed_countries.filter((country) => country !== session.selected_country && form.getAll("country").includes(country))], active: text("active") === "true", reason: text("reason"), revision: editing?.revision ?? 0};
    setSaved(false); mutation.mutate({id: editing?.subject_id, input, providerSubject: text("provider_subject")});
  };
  return <div className="page-stack operator-workspace">
    <header className="page-header"><div><h1>{t("operatorsTitle")}</h1><p>{t("operatorsHelp")}</p></div></header>
    {!session.assurance.fresh_auth && <p role="status">{t("fresh")}</p>}
    {saved && <p role="status">{t("operatorSaved")}</p>}
    {mutation.isError && <p role="alert">{mutation.error.message}</p>}
    <form className="surface-card" onSubmit={(event) => {event.preventDefault(); setSearch(formText(new FormData(event.currentTarget), "search")); setAfter("");}}>
      <label>{t("operatorSearch")}<input name="search" maxLength={128} /></label><Button>{t("searchOperators")}</Button>
      <Button type="button" onClick={() => {reset(); void query.refetch();}} disabled={mutation.isPending}>{t("refreshDirectory")}</Button>
    </form>
    {query.isPending && <p role="status">{t("operatorsLoading")}</p>}
    {query.isError && <StatePanel icon={AlertTriangle} title={t("operatorsUnavailable")} message={query.error.message} actionLabel={t("retry")} onAction={() => {void query.refetch();}} />}
    {query.data && <section className="surface-card"><h2>{t("operatorsDirectory")}</h2>
      {query.data.items.length === 0 && <p>{t("operatorsEmpty")}</p>}
      <ul className="operator-list">{query.data.items.map((account) => <li key={account.subject_id}>
        <strong>{account.display_name}</strong><p>{account.roles.map(roleLabel).join(", ")} · {account.allowed_countries.join(", ")} · {t(account.active ? "operatorActive" : "operatorSuspended")}</p>
        <small>{t("subject")}: {account.subject_id}</small>
        {!account.roles.includes("SUPER_ADMIN") && account.subject_id !== session.subject_id && <Button type="button" disabled={busy} onClick={() => {setEditing(account); setFormVersion((value) => value + 1); setSaved(false); mutation.reset();}}>{t("editOperator")}: {account.display_name}</Button>}
        <Button type="button" onClick={() => {setHistory(account); setBefore(0);}}>{t("operatorHistory")}: {account.display_name}</Button>
      </li>)}</ul>
      {after && <Button type="button" onClick={() => {setAfter("");}}>{t("firstPage")}</Button>}
      {query.data.next_after && <Button type="button" onClick={() => {setAfter(query.data.next_after ?? "");}}>{t("nextPage")}</Button>}
    </section>}
    <section className="surface-card"><h2>{t(editing ? "editOperator" : "createOperator")}{editing ? `: ${editing.display_name}` : ""}</h2>
      <form key={formVersion} onSubmit={submit}><fieldset disabled={busy}><legend>{t("operatorAccess")}</legend>
        <label>{t("name")}<input name="display_name" defaultValue={editing?.display_name ?? ""} maxLength={128} required /></label>
        {editing ? <p>{t("providerSubject")}: <code>{editing.provider_subject}</code></p> : <div><label htmlFor="operator-provider-subject">{t("providerSubject")}</label><input id="operator-provider-subject" aria-describedby="operator-provider-help" name="provider_subject" required maxLength={128} autoComplete="off" /><small id="operator-provider-help">{t("providerSubjectHelp")}</small></div>}
        <fieldset><legend>{t("operatorRoles")}</legend>{operatorRoles.map((role) => <label key={role}><input name="role" type="checkbox" value={role} defaultChecked={editing?.roles.includes(role) ?? role === "REGIONAL_STAFF"} />{roleLabel(role)}</label>)}</fieldset>
        <fieldset><legend>{t("operatorCountries")}</legend><p>{session.selected_country} ({t("currentCountry")})</p>{session.allowed_countries.filter((country) => country !== session.selected_country).map((country) => <label key={country}><input type="checkbox" name="country" value={country} defaultChecked={editing?.allowed_countries.includes(country) ?? false} />{country}</label>)}</fieldset>
        <div><label htmlFor="operator-status">{t("status")}</label><select id="operator-status" name="active" defaultValue={String(editing?.active ?? true)}><option value="true">{t("operatorActive")}</option><option value="false">{t("operatorSuspended")}</option></select></div>
        <label>{t("operatorReason")}<textarea name="reason" required minLength={8} maxLength={512} /></label>
        <Button>{t(editing ? "saveOperator" : "createOperator")}</Button>
      </fieldset></form>
      <Button type="button" disabled={mutation.isPending} onClick={reset}>{t("cancelEdit")}</Button>
    </section>
    {history && <section className="surface-card"><h2>{t("operatorHistory")}: {history.display_name}</h2>
      {events.isPending && <p role="status">{t("operatorsLoading")}</p>}
      {events.isError && <p role="alert">{events.error.message}</p>}
      {events.data && <><ul>{events.data.items.map((event) => <li key={event.id}><p>{event.action} · {event.revision} · <time dateTime={event.occurred_at}>{new Date(event.occurred_at).toLocaleString()}</time></p><p>{event.reason}</p><small>{event.actor_id} · {event.country} · {event.roles.map(roleLabel).join(", ")} · {event.allowed_countries.join(", ")} · {t(event.active ? "operatorActive" : "operatorSuspended")}</small></li>)}</ul>{events.data.items.length === 0 && <p>{t("noOperatorHistory")}</p>}{events.data.next_before && <Button type="button" onClick={() => {setBefore(events.data.next_before ?? 0);}}>{t("olderHistory")}</Button>}{before > 0 && <Button type="button" onClick={() => {setBefore(0);}}>{t("latestHistory")}</Button>}</>}
      <Button type="button" onClick={() => {setHistory(null);}}>{t("closeHistory")}</Button>
    </section>}
  </div>;
}
