import {useId, useState, type SyntheticEvent} from "react";
import {useMutation, useQuery, useQueryClient} from "@tanstack/react-query";
import type {AdminSession} from "../api/client";
import type {Participant} from "../api/participants";
import {getParticipantBinding, getParticipantTerritories, saveParticipantBinding} from "../api/participant-bindings";
import {Button} from "../components/button";
import {t} from "../lib/i18n";

export function ParticipantBindingPanel({participant, session, onClose}: {participant: Participant; session: AdminSession; onClose: () => void}) {
  const client = useQueryClient();
  const territoryID = useId();
  const scope = ["admin", "participants", session.subject_id, session.selected_country];
  const bindingKey = [...scope, "binding", participant.kind, participant.participant_id];
  const current = useQuery({queryKey: bindingKey, queryFn: ({signal}) => getParticipantBinding(participant, signal), retry: false});
  const territories = useQuery({queryKey: [...scope, "territories"], queryFn: ({signal}) => getParticipantTerritories(signal), retry: false});
  const [confirmed, setConfirmed] = useState(false);
  const mutation = useMutation({mutationFn: (value: {node_id: string; active: boolean; revision: number; reason: string}) => saveParticipantBinding(participant, value, session.csrf_token), retry: false, onSuccess: async () => {setConfirmed(true); await client.invalidateQueries({queryKey: scope});}});
  const fresh = session.assurance.fresh_auth && session.assurance.mfa_satisfied;
  const submit = (event: SyntheticEvent<HTMLFormElement>) => {
    event.preventDefault(); if (!fresh || !current.data || current.isFetching || territories.isFetching || mutation.isPending) return;
    const data = new FormData(event.currentTarget), node = data.get("node"), reason = data.get("reason");
    if (typeof node !== "string" || typeof reason !== "string" || Array.from(reason.trim()).length < 8) return;
    setConfirmed(false); mutation.mutate({node_id: node, active: data.get("active") === "on", revision: current.data.revision, reason: reason.trim()});
  };
  return <section className="surface-card"><h2>{t("participantTerritory")}: {participant.display_name}</h2><p>{t("participantTerritoryHelp")}</p>
    {(current.isPending || territories.isPending) && <p role="status">{t("participantLoading")}</p>}
    {(current.isError || territories.isError) && <p role="alert">{t("participantTerritoryFailed")}</p>}
    {current.data && territories.data && !current.isError && !territories.isError && <form key={String(current.data.revision)} onSubmit={submit}>
      <fieldset disabled={!fresh || current.isFetching || territories.isFetching || mutation.isPending || mutation.isError}><legend>{t("participantTerritory")}</legend>
        <label htmlFor={territoryID}>{t("participantTerritoryNode")}</label><select id={territoryID} name="node" defaultValue={current.data.node_id ?? ""} required><option value="">{t("select")}</option>{current.data.node_id && !territories.data.items.some(n => n.id === current.data.node_id) && <option value={current.data.node_id}>{t("masterInactive")}: {current.data.node_id}</option>}{territories.data.items.map(node => <option key={node.id} value={node.id}>{node.name}</option>)}</select>
        <label><input type="checkbox" name="active" defaultChecked={current.data.revision === 0 || current.data.active} />{t("participantTerritoryActive")}</label>
        <label>{t("masterChangeReason")}<textarea name="reason" required minLength={8} maxLength={500} /></label><Button>{t("participantTerritorySave")}</Button>
      </fieldset>
    </form>}
    {confirmed && <p role="status">{t("participantTerritorySaved")}</p>}{mutation.isError && <p role="alert">{t("participantTerritoryUnconfirmed")}</p>}
    <Button type="button" disabled={mutation.isPending || current.isFetching || territories.isFetching} onClick={() => {mutation.reset(); setConfirmed(false); void current.refetch(); void territories.refetch();}}>{t("refreshDirectory")}</Button>
    <Button type="button" disabled={mutation.isPending} onClick={onClose}>{t("cancelEdit")}</Button>
  </section>;
}
