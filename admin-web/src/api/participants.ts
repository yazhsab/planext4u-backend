import { z } from "zod";
import { requestJSON } from "./client";

export type ParticipantKind = "VENDOR" | "RIDER";
export const participantStatuses: Record<ParticipantKind, string[]> = {
  VENDOR: ["REGISTERED", "DOCUMENTS_SUBMITTED", "OCR_REVIEW", "KYC_REVIEW", "FIELD_VISIT_REQUIRED", "FIELD_VISIT_SCHEDULED", "FIELD_VISIT_PASSED", "BANK_REVIEW", "APPROVED", "REJECTED"],
  RIDER: ["REGISTERED", "KYC_REVIEW", "APPROVED", "SUSPENDED"],
};
const summary = z.object({id: z.uuid(), participant_id: z.uuid(), kind: z.enum(["VENDOR", "RIDER"]), revision: z.number().int().positive(), status: z.string().min(1), display_name: z.string().min(1), category: z.string().min(1), document_count: z.number().int().nonnegative(), bank_status: z.string(), updated_at: z.iso.datetime({offset: true}), actions: z.array(z.enum(["REJECT", "FIELD_CHECK_IN"])).max(1)});
const page = z.object({items: z.array(summary).max(50), next_after: z.uuid().optional()});
const regionalSummary = z.strictObject({id: summary.shape.id, participant_id: summary.shape.participant_id, kind: summary.shape.kind, revision: summary.shape.revision, status: summary.shape.status, display_name: summary.shape.display_name, category: summary.shape.category, updated_at: summary.shape.updated_at, actions: summary.shape.actions});
const regionalPage = z.strictObject({items: z.array(regionalSummary).max(50), next_after: z.uuid().optional()});
const receipt = z.object({participant_id: z.uuid(), kind: z.enum(["VENDOR", "RIDER"]), revision: z.number().int().positive(), status: z.enum(["REJECTED", "SUSPENDED", "FIELD_VISIT_PASSED", "OCR_REVIEW", "KYC_REVIEW", "FIELD_VISIT_REQUIRED", "BANK_REVIEW", "APPROVED"])});
const reviewAction = z.enum(["START_OCR_REVIEW", "VERIFY_KYC", "REQUIRE_FIELD_VISIT", "VERIFY_BANK", "APPROVE", "REJECT"]);
const reviewDetail = z.object({id: z.uuid(), participant_id: z.uuid(), kind: z.enum(["VENDOR", "RIDER"]), revision: z.number().int().positive(), status: z.string().min(1), display_name: z.string().min(1), category: z.string().min(1), contact_name: z.string().optional(), phone_masked: z.string().optional(), vehicle_number_masked: z.string().optional(), documents: z.array(z.object({kind: z.string().min(1), ocr_status: z.string().min(1), review_reason: z.string().optional()})).max(20), field_visit: z.object({scheduled_at: z.iso.datetime({offset: true}), checked_in_at: z.iso.datetime({offset: true}).optional(), check_in_distance_m: z.number().min(0).optional()}).optional(), service_zone_count: z.number().int().nonnegative().optional(), zones: z.array(z.string()).max(50).optional(), bank_status: z.string(), bank_last4: z.string().regex(/^[0-9]{4}$/).optional(), last_review_reason: z.string().optional(), review_actions: z.array(reviewAction).max(3), updated_at: z.iso.datetime({offset: true})});
const regionalReviewDetail = z.strictObject({id: reviewDetail.shape.id, participant_id: reviewDetail.shape.participant_id, kind: z.literal("VENDOR"), revision: reviewDetail.shape.revision, status: reviewDetail.shape.status, display_name: reviewDetail.shape.display_name, category: reviewDetail.shape.category, field_visit: z.strictObject({scheduled_at: z.iso.datetime({offset: true}), checked_in_at: z.iso.datetime({offset: true}).optional()}).optional(), service_zone_count: z.number().int().nonnegative(), updated_at: reviewDetail.shape.updated_at});
const reviewAccess = z.object({kind: z.string().regex(/^[A-Z][A-Z0-9_]{1,63}$/), url: z.url().max(8192), expires_at: z.iso.datetime({offset: true})});
export type Participant = z.infer<typeof summary>;
export type Rejection = {participant: Participant; reason: string; key: string};
export type VendorFieldCheckIn = {participant: Participant; point: {latitude: number; longitude: number}; key: string};
export type ParticipantReviewDetail = z.infer<typeof reviewDetail>;
export type ParticipantReviewAction = z.infer<typeof reviewAction>;
export type ReviewDecision = {detail: ParticipantReviewDetail; action: ParticipantReviewAction; reason: string; key: string};
const targetStatus: Record<ParticipantReviewAction, string> = {START_OCR_REVIEW: "OCR_REVIEW", VERIFY_KYC: "KYC_REVIEW", REQUIRE_FIELD_VISIT: "FIELD_VISIT_REQUIRED", VERIFY_BANK: "BANK_REVIEW", APPROVE: "APPROVED", REJECT: "REJECTED"};
export const getParticipants = async (kind: ParticipantKind, status: string, search: string, after: string, signal?: AbortSignal, regionalRead = false) => {
  const path = `/admin/api/v1/participants/${kind}?${new URLSearchParams({status, search, after})}`;
  const regionalResult = regionalRead ? await requestJSON(path, regionalPage, {signal}) : null;
  const result = regionalResult
    ? {...regionalResult, items: regionalResult.items.map((item) => ({...item, document_count: 0, bank_status: ""}))}
    : await requestJSON(path, page, {signal});
  if (result.items.some((item) => item.kind !== kind || !participantStatuses[kind].includes(item.status) || item.actions.includes("FIELD_CHECK_IN") && (kind !== "VENDOR" || item.status !== "FIELD_VISIT_SCHEDULED")) || result.next_after && (result.items.length !== 50 || result.items.at(49)?.id !== result.next_after)) throw new Error("Invalid participant directory response.");
  return result;
};
export const recordVendorFieldCheckIn = async (command: VendorFieldCheckIn, csrf: string, signal?: AbortSignal) => {
  const p = command.participant;
  if (p.kind !== "VENDOR" || p.status !== "FIELD_VISIT_SCHEDULED" || !p.actions.includes("FIELD_CHECK_IN")) throw new Error("The vendor visit is not currently eligible.");
  const point = z.object({latitude: z.number().min(-90).max(90), longitude: z.number().min(-180).max(180)}).parse(command.point);
  const result = await requestJSON(`/admin/api/v1/participants/VENDOR/${z.uuid().parse(p.participant_id)}/field-check-in`, receipt, {method: "POST", headers: {"Content-Type": "application/json", "X-CSRF-Token": csrf, "Idempotency-Key": command.key, "If-Match": `"${String(p.revision)}"`}, body: JSON.stringify(point), signal});
  if (result.kind !== "VENDOR" || result.participant_id !== p.participant_id || result.revision !== p.revision + 1 || result.status !== "FIELD_VISIT_PASSED") throw new Error("Vendor field visit response did not match.");
  return result;
};
export const getParticipantReviewDetail = async (kind: ParticipantKind, participantID: string, signal?: AbortSignal, regionalRead = false) => {
  const path = `/admin/api/v1/participants/${kind}/${z.uuid().parse(participantID)}/review`;
  const result: ParticipantReviewDetail = regionalRead ? {...await requestJSON(path, regionalReviewDetail, {signal}), documents: [], bank_status: "", review_actions: []} : await requestJSON(path, reviewDetail, {signal});
  if (result.kind !== kind || result.participant_id !== participantID || !participantStatuses[kind].includes(result.status) || result.review_actions.some((action) => kind === "RIDER" ? result.status !== "KYC_REVIEW" || action !== "APPROVE" && action !== "REJECT" : action === "START_OCR_REVIEW" ? result.status !== "DOCUMENTS_SUBMITTED" : action === "VERIFY_KYC" ? result.status !== "OCR_REVIEW" : action === "REQUIRE_FIELD_VISIT" ? result.status !== "KYC_REVIEW" : action === "VERIFY_BANK" || action === "APPROVE" ? result.status !== "BANK_REVIEW" : result.status !== "OCR_REVIEW" && result.status !== "KYC_REVIEW" && result.status !== "BANK_REVIEW")) throw new Error("Invalid participant review detail.");
  if (regionalRead && (kind !== "VENDOR" || result.contact_name || result.phone_masked || result.vehicle_number_masked || result.documents.length !== 0 || result.bank_status || result.bank_last4 || result.last_review_reason || result.zones?.length || result.review_actions.length !== 0)) throw new Error("Invalid regional participant review detail.");
  return result;
};
export const getParticipantReviewDocumentAccess = async (detail: ParticipantReviewDetail, documentKind: string, csrf: string, signal?: AbortSignal) => {
  const kind = reviewAccess.shape.kind.parse(documentKind);
  if (detail.documents.filter((document) => document.kind === kind).length !== 1 || !participantStatuses[detail.kind].includes(detail.status)) throw new Error("The current application does not contain one reviewable document of this kind.");
  const result = await requestJSON(`/admin/api/v1/participants/${detail.kind}/${z.uuid().parse(detail.participant_id)}/review/documents/${kind}/access`, reviewAccess, {method: "POST", headers: {"X-CSRF-Token": csrf}, signal});
  const url = new URL(result.url);
  const expiry = Date.parse(result.expires_at);
  const loopback = url.hostname === "localhost" || url.hostname === "127.0.0.1" || url.hostname === "[::1]";
  if (result.kind !== kind || url.username || url.password || url.hash || !url.search || url.protocol !== "https:" && !(url.protocol === "http:" && loopback) || expiry <= Date.now() || expiry > Date.now() + 125000) throw new Error("Invalid private document access grant.");
  return result;
};
export const recordParticipantReviewAction = async (command: ReviewDecision, csrf: string, signal?: AbortSignal) => {
  const {detail, action, reason, key} = command;
  if (!detail.review_actions.includes(action) || Array.from(reason.trim()).length < 8 || detail.kind === "RIDER" && action !== "APPROVE" && action !== "REJECT") throw new Error("The review action is not currently eligible.");
  const result = await requestJSON(`/admin/api/v1/participants/${detail.kind}/${z.uuid().parse(detail.participant_id)}/review-action`, receipt, {method: "POST", headers: {"Content-Type": "application/json", "X-CSRF-Token": csrf, "Idempotency-Key": key, "If-Match": `"${String(detail.revision)}"`}, body: JSON.stringify({action, reason: reason.trim()}), signal});
  const expected = detail.kind === "RIDER" && action === "REJECT" ? "SUSPENDED" : targetStatus[action];
  if (result.kind !== detail.kind || result.participant_id !== detail.participant_id || result.revision !== detail.revision + 1 || result.status !== expected) throw new Error("Participant review action response did not match.");
  return result;
};
export const rejectParticipant = async (command: Rejection, csrf: string) => {
  const p = command.participant;
  const result = await requestJSON(`/admin/api/v1/participants/${p.kind}/${z.uuid().parse(p.participant_id)}/reject`, receipt, {method: "POST", headers: {"Content-Type": "application/json", "X-CSRF-Token": csrf, "Idempotency-Key": command.key, "If-Match": `"${String(p.revision)}"`}, body: JSON.stringify({reason: command.reason})});
  if (result.kind !== p.kind || result.participant_id !== p.participant_id || result.revision !== p.revision + 1 || result.status !== (p.kind === "RIDER" ? "SUSPENDED" : "REJECTED")) throw new Error("Participant command response did not match.");
  return result;
};
