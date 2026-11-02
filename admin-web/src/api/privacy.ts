import {z} from "zod";
import {requestJSON} from "./client";

const identifier = z.string().regex(/^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/);
const owner = z.string().regex(/^[a-z_]{1,128}$/);
const reason = z.string().trim().min(8).max(256);
const policyVersion = identifier;
const instant = z.iso.datetime({offset: true});
const count = z.number().int().nonnegative();
const proposal = z.object({
  id: z.uuid(), owner, expected_revision: count, policy_version: policyVersion,
  retention_microseconds: count.max(Number.MAX_SAFE_INTEGER), reason_reference: reason,
  status: z.enum(["PENDING", "PUBLISHED"]), requested_by: z.uuid(), requested_at: instant,
  approved_by: z.uuid().optional(), published_at: instant.optional(),
});
const deletion = z.object({
  id: identifier, identity_id: z.uuid(), status: z.string().min(1).max(32),
  requested_at: instant, effective_at: instant, ready_owners: count,
  blocked_owners: count, active_holds: count,
});
const hold = z.object({
  id: z.number().int().positive(), request_id: identifier, owner,
  reason_reference: reason, recorded_by: z.uuid().optional(),
  recorded_at: instant, revision: z.number().int().positive(),
});
const proposals = z.object({items: z.array(proposal).max(100)});
const deletions = z.object({items: z.array(deletion).max(100)});
const holds = z.object({items: z.array(hold).max(100)});
const proposed = z.object({id: z.uuid(), status: z.literal("PROPOSED")});
const published = z.object({id: z.uuid(), status: z.literal("PUBLISHED"), revision: z.number().int().positive()});
const placed = z.object({id: z.number().int().positive(), status: z.literal("ACTIVE"), revision: z.literal(1)});
const released = z.object({id: z.number().int().positive(), status: z.literal("RELEASED"), revision: z.number().int().positive()});

export type PrivacyProposal = z.infer<typeof proposal>;
export type PrivacyDeletion = z.infer<typeof deletion>;
export type PrivacyHold = z.infer<typeof hold>;
export type PrivacyProposalInput = {id: string; owner: string; policy_version: string; retention_seconds: number; expected_revision: number; reason_reference: string};

export const listPrivacyProposals = (signal?: AbortSignal) => requestJSON("/admin/api/v1/privacy/retention-proposals", proposals, {signal});
export const listPrivacyDeletions = (signal?: AbortSignal) => requestJSON("/admin/api/v1/privacy/deletions", deletions, {signal});
export const listPrivacyHolds = (signal?: AbortSignal) => requestJSON("/admin/api/v1/privacy/holds", holds, {signal});
export const getPrivacyProposal = (id: string, signal?: AbortSignal) => requestJSON(`/admin/api/v1/privacy/retention-proposals/${z.uuid().parse(id)}`, proposal, {signal});
export const getPrivacyDeletion = (id: string, signal?: AbortSignal) => requestJSON(`/admin/api/v1/privacy/deletions/${identifier.parse(id)}`, deletion, {signal});

export const proposePrivacyRetention = (input: PrivacyProposalInput, csrf: string, signal?: AbortSignal) => {
  const valid = z.object({id: z.uuid(), owner, policy_version: policyVersion, retention_seconds: count.max(3_153_600_000), expected_revision: count, reason_reference: reason}).parse(input);
  return requestJSON("/admin/api/v1/privacy/retention-proposals", proposed, {
    method: "POST", headers: {"Content-Type": "application/json", "X-CSRF-Token": csrf},
    body: JSON.stringify(valid), signal,
  });
};
export const publishPrivacyRetention = (id: string, csrf: string, signal?: AbortSignal) => requestJSON(`/admin/api/v1/privacy/retention-proposals/${z.uuid().parse(id)}/publish`, published, {method: "POST", headers: {"X-CSRF-Token": csrf}, signal});
export const placePrivacyHold = (requestID: string, input: {owner: string; reason_reference: string}, csrf: string, signal?: AbortSignal) => requestJSON(`/admin/api/v1/privacy/deletions/${identifier.parse(requestID)}/holds`, placed, {method: "POST", headers: {"Content-Type": "application/json", "X-CSRF-Token": csrf}, body: JSON.stringify(z.object({owner, reason_reference: reason}).parse(input)), signal});
export const releasePrivacyHold = (id: number, expectedRevision: number, reasonReference: string, csrf: string, signal?: AbortSignal) => requestJSON(`/admin/api/v1/privacy/holds/${String(z.number().int().positive().parse(id))}/release`, released, {method: "POST", headers: {"Content-Type": "application/json", "X-CSRF-Token": csrf}, body: JSON.stringify({expected_revision: z.number().int().positive().parse(expectedRevision), reason_reference: reason.parse(reasonReference)}), signal});
