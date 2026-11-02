import { z } from "zod";
import { requestJSON } from "./client";

export const masterKinds = ["COUNTRY", "STATE", "DISTRICT", "CITY", "AREA", "OCCUPATION", "REASON"] as const;
export type MasterKind = typeof masterKinds[number];
export const masterParents: Partial<Record<MasterKind, MasterKind>> = {STATE: "COUNTRY", DISTRICT: "STATE", CITY: "DISTRICT", AREA: "CITY"};
const master = z.object({id: z.uuid(), kind: z.enum(masterKinds), code: z.string().regex(/^[A-Z][A-Z0-9_]{0,63}$/), parent_id: z.uuid().optional(), name_en: z.string().min(1).max(128), name_ta: z.string().min(1).max(128), active: z.boolean(), revision: z.number().int().positive()});
const page = z.object({items: z.array(master).max(50), next_after: z.uuid().optional()});
const history = z.object({items: z.array(z.object({definition: master, actor_id: z.uuid(), reason: z.string().min(8).max(500), occurred_at: z.iso.datetime({offset: true})})).max(50), next_before: z.number().int().positive().optional()});
export type Master = z.infer<typeof master>;
export type MasterChange = Omit<Master, "revision"> & {revision: number; reason: string};
export const getMasters = (kind: MasterKind, search: string, status: string, after: string, signal?: AbortSignal) => requestJSON(`/admin/api/v1/masters?${new URLSearchParams({kind, search, status, after})}`, page, {signal});
export const saveMaster = (value: MasterChange, csrf: string) => requestJSON(`/admin/api/v1/masters/${z.uuid().parse(value.id)}`, master, {method: "PUT", headers: {"Content-Type": "application/json", "X-CSRF-Token": csrf}, body: JSON.stringify(value)});
export const getMasterHistory = (id: string, before: number, signal?: AbortSignal) => requestJSON(`/admin/api/v1/masters/${z.uuid().parse(id)}/history?${new URLSearchParams({before: String(before)})}`, history, {signal});
