import { z } from "zod";
import { requestJSON } from "./client";

export const operatorRoles = ["COUNTRY_ADMIN", "FINANCE", "CONTENT_ADMIN", "SUPPORT_ADMIN", "AUDITOR", "REGIONAL_STAFF"] as const;
const role = z.enum(["SUPER_ADMIN", ...operatorRoles]);
const country = z.string().regex(/^[A-Z]{2}$/);
const account = z.object({subject_id: z.uuid(), provider_subject: z.string().min(1).max(128), display_name: z.string().min(1).max(128), roles: z.array(role).min(1).max(8), allowed_countries: z.array(country).min(1).max(32), active: z.boolean(), revision: z.number().int().positive()});
const page = z.object({items: z.array(account).max(50), next_after: z.uuid().optional()});
const event = z.object({id: z.uuid(), actor_id: z.uuid(), country, action: z.enum(["CREATED", "UPDATED"]), revision: z.number().int().positive(), roles: z.array(role), allowed_countries: z.array(country), active: z.boolean(), reason: z.string(), occurred_at: z.iso.datetime({offset: true})});
const events = z.object({items: z.array(event).max(50), next_before: z.number().int().positive().optional()});
export type OperatorAccount = z.infer<typeof account>;
export type OperatorChange = {display_name: string; roles: typeof operatorRoles[number][]; allowed_countries: string[]; active: boolean; revision: number; reason: string};
const body = (value: unknown, csrf: string, method: string): RequestInit => ({method, headers: {"Content-Type": "application/json", "X-CSRF-Token": csrf}, body: JSON.stringify(value)});
export const getOperators = (search: string, after: string, signal?: AbortSignal) => requestJSON(`/admin/api/v1/operators?${new URLSearchParams({search, after})}`, page, {signal});
export const createOperator = (value: OperatorChange & {provider_subject: string}, csrf: string) => requestJSON("/admin/api/v1/operators", account, body(value, csrf, "POST"));
export const updateOperator = (id: string, value: OperatorChange, csrf: string) => requestJSON(`/admin/api/v1/operators/${encodeURIComponent(z.uuid().parse(id))}`, account, body(value, csrf, "PUT"));
export const getOperatorEvents = (id: string, before: number, signal?: AbortSignal) => requestJSON(`/admin/api/v1/operators/${encodeURIComponent(z.uuid().parse(id))}/events?${new URLSearchParams({before: String(before)})}`, events, {signal});
