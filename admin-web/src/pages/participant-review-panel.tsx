import {useEffect, useRef, useState, type SyntheticEvent} from "react";
import {useMutation, useQuery, useQueryClient} from "@tanstack/react-query";
import {APIError, type AdminSession} from "../api/client";
import {getParticipantReviewDetail, getParticipantReviewDocumentAccess, recordParticipantReviewAction, type Participant, type ParticipantReviewAction, type ReviewDecision} from "../api/participants";
import {Button} from "../components/button";
import {t} from "../lib/i18n";

const actionLabels: Record<ParticipantReviewAction, () => string> = {
  START_OCR_REVIEW: () => t("participantStartOCR"),
  VERIFY_KYC: () => t("participantVerifyKYC"),
  REQUIRE_FIELD_VISIT: () => t("participantRequireVisit"),
  VERIFY_BANK: () => t("participantVerifyBank"),
  APPROVE: () => t("participantApprove"),
  REJECT: () => t("participantReject"),
};

export function ParticipantReviewPanel({participant, session, onClose, onRecorded}: {participant: Participant; session: AdminSession; onClose: () => void; onRecorded: (status: string, revision: number) => void}) {
  const client = useQueryClient();
  const country = session.roles.includes("COUNTRY_ADMIN") || session.roles.includes("SUPER_ADMIN");
  const regional = !country && participant.kind === "VENDOR" && session.roles.includes("REGIONAL_STAFF");
  const canRead = country || regional;
  const fresh = session.assurance.fresh_auth && session.assurance.mfa_satisfied;
  const accessClass = country ? "country" : regional ? "regional" : "denied";
  const key = ["admin", "participants", "review", session.subject_id, session.selected_country, session.assurance.auth_time, accessClass, participant.kind, participant.participant_id];
  const query = useQuery({queryKey: key, queryFn: ({signal}) => getParticipantReviewDetail(participant.kind, participant.participant_id, signal, regional), enabled: canRead && fresh, gcTime: 0, refetchOnMount: "always", retry: false});
  const [action, setAction] = useState<ParticipantReviewAction | null>(null);
  const [command, setCommand] = useState<ReviewDecision | null>(null);
  const [evidencePending, setEvidencePending] = useState<string | null>(null);
  const [evidenceError, setEvidenceError] = useState("");
  const evidencePopup = useRef<Window | null>(null);
  const activeReviewerKey = `${session.subject_id}:${session.selected_country}:${session.assurance.auth_time}:${accessClass}:${String(fresh)}:${participant.kind}:${participant.participant_id}:${String(query.data?.revision ?? 0)}`;
  const activeReviewerKeyRef = useRef(activeReviewerKey);
  activeReviewerKeyRef.current = activeReviewerKey;
  useEffect(() => () => {evidencePopup.current?.close(); evidencePopup.current = null;}, [activeReviewerKey]);
  const mutation = useMutation({mutationFn: (value: ReviewDecision) => recordParticipantReviewAction(value, session.csrf_token), retry: false, onSuccess: async (receipt) => {
    onRecorded(receipt.status, receipt.revision);
    await client.invalidateQueries({queryKey: ["admin", "participants", session.subject_id, session.selected_country]});
  }});
  const definitive = mutation.error instanceof APIError && mutation.error.status < 500;
  const submit = (event: SyntheticEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!country || !query.data || !action || command || !fresh) return;
    const reason = new FormData(event.currentTarget).get("reason");
    if (typeof reason !== "string" || Array.from(reason.trim()).length < 8) return;
    const value = {detail: query.data, action, reason: reason.trim(), key: crypto.randomUUID()};
    setCommand(value); mutation.mutate(value);
  };
  const inspect = async (kind: string) => {
    if (!country || !fresh || !query.data || evidencePending !== null) return;
    setEvidenceError("");
    const opened = window.open("about:blank", "_blank");
    if (!opened) {setEvidenceError(t("participantEvidencePopupBlocked")); return;}
    opened.opener = null;
    evidencePopup.current = opened;
    setEvidencePending(kind);
    const reviewerKey = activeReviewerKey;
    try {
      const grant = await getParticipantReviewDocumentAccess(query.data, kind, session.csrf_token);
      if (activeReviewerKeyRef.current !== reviewerKey || opened.closed) {opened.close(); return;}
      opened.location.replace(grant.url);
      evidencePopup.current = null;
    } catch (error) {
      opened.close();
      if (activeReviewerKeyRef.current === reviewerKey) setEvidenceError(error instanceof Error ? error.message : t("participantEvidenceUnavailable"));
    } finally {
      if (activeReviewerKeyRef.current === reviewerKey) setEvidencePending(null);
    }
  };
  if (!canRead) return <p role="alert">{t("participantsDenied")}</p>;
  return <section className="surface-card" aria-label={t("participantReviewTitle")}>
    <h2>{t("participantReviewTitle")}: {participant.display_name}</h2>
    {!fresh && <p role="alert">{t("participantFresh")}</p>}
    {fresh && (query.isPending || query.isFetching) && <p role="status">{t("participantLoading")}</p>}
    {fresh && query.isError && <p role="alert">{query.error.message} <Button type="button" onClick={() => {void query.refetch();}}>{t("retry")}</Button></p>}
    {fresh && query.data && !query.isError && !query.isFetching && <>
      <p>{t("status")}: {query.data.status} · {t("audienceRevision")}: {query.data.revision}</p>
      <p>{query.data.category}{query.data.contact_name ? ` · ${query.data.contact_name}` : ""}</p>
      {query.data.phone_masked && <p>{t("participantReviewPhone")}: {query.data.phone_masked}</p>}
      {query.data.vehicle_number_masked && <p>{t("participantReviewVehicle")}: {query.data.vehicle_number_masked}</p>}
      {country && <><h3>{t("participantReviewEvidence")}</h3>
        <p role="note">{t("participantReviewEvidenceLimit")}</p>
        {query.data.documents.length === 0 ? <p>{t("participantNotSubmitted")}</p> : <ul>{query.data.documents.map((document, index) => <li key={`${document.kind}:${String(index)}`}>{document.kind}: {document.ocr_status}{document.review_reason ? ` · ${document.review_reason}` : ""} <Button type="button" disabled={evidencePending !== null || command !== null} onClick={() => {void inspect(document.kind);}}>{t("participantOpenEvidence")}: {document.kind}</Button></li>)}</ul>}
        {evidencePending && <p role="status">{t("participantEvidenceLoading")}</p>}
        {evidenceError && <p role="alert">{evidenceError}</p>}</>}
      {query.data.field_visit && <p>{t("participantReviewVisit")}: {new Date(query.data.field_visit.scheduled_at).toLocaleString()} · {query.data.field_visit.checked_in_at ? t("participantFieldRecorded") : t("participantReviewVisitPending")}</p>}
      {query.data.service_zone_count !== undefined && <p>{t("participantReviewZones")}: {query.data.service_zone_count}</p>}
      {query.data.zones && <p>{t("participantReviewZones")}: {query.data.zones.join(", ") || t("participantNotSubmitted")}</p>}
      {regional && <p role="note">{t("participantRegionalSummary")}</p>}
      {country && <><p>{t("participantBank")}: {query.data.bank_status || t("participantNotSubmitted")}{query.data.bank_last4 ? ` · ••••${query.data.bank_last4}` : ""}</p>
        {query.data.last_review_reason && <p>{t("participantReviewLastReason")}: {query.data.last_review_reason}</p>}
        <p>{t("participantReviewHelp")}</p>
        <div className="field-actions">{query.data.review_actions.filter((candidate) => candidate !== "VERIFY_BANK" || session.capabilities.includes("admin.participants.bank_verify")).map((candidate) => <Button key={candidate} type="button" disabled={command !== null} onClick={() => {setAction(candidate); mutation.reset();}}>{actionLabels[candidate]()}</Button>)}</div>
        {action && <form onSubmit={submit}><h3>{actionLabels[action]()}</h3><label>{t("masterChangeReason")}<textarea name="reason" minLength={8} maxLength={512} required readOnly={command !== null} /></label><Button disabled={command !== null}>{t("participantReviewConfirm")}</Button><Button type="button" disabled={mutation.isPending || command !== null && !definitive} onClick={() => {setAction(null); setCommand(null); mutation.reset();}}>{t("cancelEdit")}</Button></form>}
        {mutation.isPending && <p role="status">{t("participantSaving")}</p>}
        {mutation.isError && <p role="alert">{mutation.error.message} {definitive ? t("participantRefreshConflict") : t("participantUnconfirmed")}</p>}
        {mutation.isError && command && !definitive && <Button type="button" disabled={!fresh} onClick={() => {mutation.mutate(command);}}>{t("participantRetryCommand")}</Button>}
        {mutation.isError && definitive && <Button type="button" onClick={() => {setAction(null); setCommand(null); mutation.reset(); void query.refetch();}}>{t("refreshDirectory")}</Button>}</>}
    </>}
    <Button type="button" disabled={mutation.isPending} onClick={onClose}>{t("cancelEdit")}</Button>
  </section>;
}
