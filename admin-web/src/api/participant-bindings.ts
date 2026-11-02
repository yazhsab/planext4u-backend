import {z} from "zod";
import {requestJSON} from "./client";
import type {Participant} from "./participants";

const binding = z.object({kind: z.enum(["VENDOR", "RIDER"]), participant_id: z.uuid(), node_id: z.uuid().optional(), active: z.boolean(), revision: z.number().int().nonnegative()});
export type ParticipantBinding = z.infer<typeof binding>;
const path = (p: Participant) => `/admin/api/v1/participants/${p.kind}/${z.uuid().parse(p.participant_id)}/binding`;
export const getParticipantBinding = (p: Participant, signal?: AbortSignal) => requestJSON(path(p), binding, {signal});
export const getParticipantTerritories = (signal?: AbortSignal) => requestJSON("/admin/api/v1/participant-territories", z.object({items: z.array(z.object({id: z.uuid(), name: z.string().min(1)})).max(1000)}), {signal});
export const saveParticipantBinding = async (p: Participant, value: {node_id: string; active: boolean; revision: number; reason: string}, csrf: string) => {
  const result = await requestJSON(path(p), binding, {method: "PUT", headers: {"Content-Type": "application/json", "X-CSRF-Token": csrf}, body: JSON.stringify(value)});
  if (result.kind !== p.kind || result.participant_id !== p.participant_id || result.node_id !== value.node_id || result.active !== value.active || result.revision !== value.revision + 1) throw new Error("Participant binding response did not match.");
  return result;
};
