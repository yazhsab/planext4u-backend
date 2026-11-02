import { useState, type SyntheticEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useOutletContext } from "react-router";
import { APIError, type AdminSession } from "../api/client";
import { getParticipants, participantStatuses, rejectParticipant, type Participant, type ParticipantKind, type Rejection } from "../api/participants";
import { Button } from "../components/button";
import { t } from "../lib/i18n";

import {ParticipantBindingPanel} from "./participant-binding-panel";
import {ParticipantFieldCheckIn} from "./participant-field-check-in";
import {ParticipantReviewPanel} from "./participant-review-panel";

export function ParticipantsPage() {
  const session = useOutletContext<AdminSession>();
  if (!session.capabilities.includes("admin.participants.review")) return <p role="alert">{t("participantsDenied")}</p>;
  return <ParticipantWorkspace key={`${session.subject_id}:${session.selected_country}:${session.roles.join(",")}:${session.capabilities.join(",")}`} session={session} />;
}
const formText = (form: FormData, name: string) => {const value = form.get(name); return typeof value === "string" ? value.trim() : "";};
function ParticipantWorkspace({session}: {session: AdminSession}) {
  const client = useQueryClient();
  const [kind, setKind] = useState<ParticipantKind>("VENDOR");
  const [filter, setFilter] = useState({status: "", search: "", after: ""});
  const [selected, setSelected] = useState<Participant | null>(null);
  const [command, setCommand] = useState<Rejection | null>(null);
  const [confirmed, setConfirmed] = useState("");
  const [bindingParticipant,setBindingParticipant]=useState<Participant|null>(null);
  const [fieldParticipant,setFieldParticipant]=useState<Participant|null>(null);
  const [reviewParticipant,setReviewParticipant]=useState<Participant|null>(null);
  const scope = ["admin", "participants", session.subject_id, session.selected_country, session.roles.join(","), session.capabilities.join(",")];
  const fresh = session.assurance.fresh_auth && session.assurance.mfa_satisfied;
  const regionalRead = session.roles.includes("REGIONAL_STAFF") && !session.roles.includes("COUNTRY_ADMIN") && !session.roles.includes("SUPER_ADMIN");
  const query = useQuery({queryKey: [...scope, session.assurance.auth_time, kind, filter], queryFn: ({signal}) => getParticipants(kind, filter.status, filter.search, filter.after, signal, regionalRead), enabled: fresh, gcTime: 0, refetchOnMount: "always", retry: false});
  const mutation = useMutation({mutationFn: (value: Rejection) => rejectParticipant(value, session.csrf_token), retry: false, onSuccess: async (receipt) => {
    setConfirmed(`${t("participantConfirmed")}: ${receipt.status} (${String(receipt.revision)})`); setSelected(null); setCommand(null);
    await client.invalidateQueries({queryKey: scope});
  }});
  const locked = mutation.isPending || command !== null;
  const definitive = mutation.error instanceof APIError && mutation.error.status < 500;
  const reset = () => {setBindingParticipant(null); setFieldParticipant(null); setReviewParticipant(null); setSelected(null); setCommand(null); mutation.reset();};
  const submit = (event: SyntheticEvent<HTMLFormElement>) => {
    event.preventDefault(); if (!selected || !fresh || command) return;
    const reason = new FormData(event.currentTarget).get("reason");
    if (typeof reason !== "string" || Array.from(reason.trim()).length < 8) return;
    const value = {participant: selected, reason: reason.trim(), key: crypto.randomUUID()}; setCommand(value); setConfirmed(""); mutation.mutate(value);
  };
  return <div className="page-stack operator-workspace"><header className="page-header"><div><h1>{t("participantsTitle")}</h1><p>{t("participantsHelp")}</p></div></header>
    {!fresh && <p role="alert">{t("participantFresh")}</p>}{confirmed && <p role="status">{confirmed}</p>}
    <form className="surface-card" onSubmit={(event) => {event.preventDefault(); if (locked) return; const form = new FormData(event.currentTarget); reset(); setFilter({status: formText(form, "status"), search: formText(form, "search"), after: ""});}}>
      <fieldset disabled={locked}><legend>{t("participantFilters")}</legend>
        <label>{t("participantKind")}<select value={kind} aria-label={t("participantKind")} onChange={(event) => {reset(); setKind(event.target.value as ParticipantKind); setFilter({status: "", search: "", after: ""});}}><option value="VENDOR">{t("participantVendor")}</option><option value="RIDER">{t("participantRider")}</option></select></label>
        <label>{t("status")}<select name="status" aria-label={t("status")} key={kind} defaultValue=""><option value="">{t("masterAll")}</option>{participantStatuses[kind].map((status) => <option key={status}>{status}</option>)}</select></label>
        <label>{t("participantSearch")}<input name="search" maxLength={128} /></label><Button>{t("participantFind")}</Button>
      </fieldset>
      <Button type="button" disabled={mutation.isPending || command !== null && !definitive} onClick={() => {reset(); void query.refetch();}}>{t("refreshDirectory")}</Button>
    </form>
    {fresh && (query.isPending || query.isFetching) && <p role="status">{t("participantLoading")}</p>}{fresh && query.isError && <p role="alert">{query.error.message}<Button type="button" onClick={() => {void query.refetch();}}>{t("retry")}</Button></p>}
    {fresh && query.data && !query.isError && !query.isFetching && <section className="surface-card"><h2>{t(kind === "VENDOR" ? "participantVendor" : "participantRider")}</h2>{query.data.items.length === 0 && <p>{t("participantEmpty")}</p>}
      <ul className="operator-list">{query.data.items.map((p) => <li key={p.id}><strong>{p.display_name}</strong><p>{p.status} · {p.category}</p>{!regionalRead && <p>{t("participantDocuments")}: {p.document_count} · {t("participantBank")}: {p.bank_status || t("participantNotSubmitted")}</p>}<small>{new Date(p.updated_at).toLocaleString()}</small>
        {session.capabilities.includes("admin.participants.bind") && <Button type="button" disabled={locked} onClick={() => {setBindingParticipant(p);}}>{t("participantTerritory")}: {p.display_name}</Button>}
        {(session.roles.includes("COUNTRY_ADMIN") || session.roles.includes("SUPER_ADMIN") || p.kind === "VENDOR" && session.roles.includes("REGIONAL_STAFF")) && p.participant_id !== session.subject_id && <Button type="button" disabled={locked} onClick={() => {setReviewParticipant(p); setSelected(null); setFieldParticipant(null); setBindingParticipant(null); setConfirmed("");}}>{t("participantReviewTitle")}: {p.display_name}</Button>}
        {session.capabilities.includes("admin.participants.field_checkin") && p.actions.includes("FIELD_CHECK_IN") && p.participant_id !== session.subject_id && <Button type="button" disabled={locked} onClick={() => {setFieldParticipant(p); setSelected(null); setBindingParticipant(null); setConfirmed("");}}>{t("participantFieldTitle")}: {p.display_name}</Button>}
        {p.actions.includes("REJECT") && p.participant_id !== session.subject_id && <Button type="button" disabled={locked} onClick={() => {setSelected(p); mutation.reset(); setConfirmed("");}}>{t("participantReject")}: {p.display_name}</Button>}
      </li>)}</ul>
      {filter.after && <Button type="button" disabled={locked} onClick={() => {setFilter({...filter, after: ""});}}>{t("firstPage")}</Button>}{query.data.next_after && <Button type="button" disabled={locked} onClick={() => {setFilter({...filter, after: query.data.next_after ?? ""});}}>{t("nextPage")}</Button>}
    </section>}
    {bindingParticipant && <ParticipantBindingPanel key={`${bindingParticipant.kind}:${bindingParticipant.participant_id}`} participant={bindingParticipant} session={session} onClose={() => {setBindingParticipant(null);}} />}
    {fieldParticipant && <ParticipantFieldCheckIn key={`${fieldParticipant.participant_id}:${String(fieldParticipant.revision)}`} participant={fieldParticipant} session={session} onRecorded={() => {setConfirmed(t("participantFieldRecorded")); setFieldParticipant(null); void client.invalidateQueries({queryKey: scope});}} onDenied={() => {setFieldParticipant(null); void client.invalidateQueries({queryKey: scope});}} onClose={() => {setFieldParticipant(null);}} />}
    {reviewParticipant && <ParticipantReviewPanel key={`${reviewParticipant.kind}:${reviewParticipant.participant_id}`} participant={reviewParticipant} session={session} onClose={() => {setReviewParticipant(null);}} onRecorded={(status, revision) => {setConfirmed(`${t("participantConfirmed")}: ${status} (${String(revision)})`); setReviewParticipant(null);}} />}
    {selected && <section className="surface-card"><h2>{t("participantReject")}: {selected.display_name}</h2><p>{t("participantRejectHelp")}</p>
      <form onSubmit={submit}><label>{t("masterChangeReason")}<textarea name="reason" minLength={8} maxLength={512} required readOnly={locked} /></label><Button disabled={!fresh || locked}>{t("participantConfirmReject")}</Button><Button type="button" disabled={locked} onClick={reset}>{t("cancelEdit")}</Button></form>
      {mutation.isPending && <p role="status">{t("participantSaving")}</p>}{mutation.isError && <p role="alert">{mutation.error.message} {definitive ? t("participantRefreshConflict") : t("participantUnconfirmed")}</p>}
      {mutation.isError && command && !definitive && <Button type="button" disabled={!fresh} onClick={() => {mutation.mutate(command);}}>{t("participantRetryCommand")}</Button>}
    </section>}
  </div>;
}
