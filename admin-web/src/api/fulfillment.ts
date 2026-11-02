import {z} from "zod";
import {requestJSON} from "./client";

const territory = z.object({id: z.uuid(), name: z.string().min(1), region_id: z.string().min(1)});
const territories = z.object({items: z.array(territory)});
const count = z.number().int().nonnegative();
const dashboard = z.object({region_id: z.string(), territories: count, online_riders: count, unassigned_tasks: count, at_risk_tasks: count, completed_today: count, generated_at: z.iso.datetime({offset: true})});
export const getFulfillmentTerritories = (signal?: AbortSignal) => requestJSON("/admin/api/v1/fulfillment/territories", territories, {signal});
export const getFulfillmentDashboard = (region: string, signal?: AbortSignal) => requestJSON(`/admin/api/v1/fulfillment/regions/${encodeURIComponent(z.string().regex(/^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/).parse(region))}`, dashboard, {signal});

export async function getRegionalSummaryCSV(region:string,signal:AbortSignal):Promise<Blob>{
 const id=z.string().regex(/^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/).parse(region);
 const response=await fetch(`/admin/api/v1/fulfillment/regions/${encodeURIComponent(id)}/summary-export`,{signal,credentials:"same-origin",cache:"no-store",redirect:"error",headers:{Accept:"text/csv"}});
 if(!response.ok || response.headers.get("content-type")?.split(";")[0]!=="text/csv" || !response.body)throw new Error("Regional export unavailable");
 const reader=response.body.getReader(),chunks:Uint8Array<ArrayBuffer>[]= [];let size=0;
 try{for(;;){const value=await reader.read();if(value.done)break;size+=value.value.byteLength;if(size>65536)throw new Error("Regional export exceeds its limit");chunks.push(new Uint8Array(value.value));}}
 finally{await reader.cancel();reader.releaseLock();}
 if(size===0)throw new Error("Regional export is empty");
 return new Blob(chunks,{type:"text/csv;charset=utf-8"});
}
export const getFieldTerritories = (signal?: AbortSignal) => requestJSON("/admin/api/v1/fulfillment/field-territories", z.object({territory_ids: z.array(z.uuid()).max(2000)}), {signal});
const point = z.object({latitude: z.number().min(-90).max(90),longitude: z.number().min(-180).max(180)});
const visit = z.object({id: z.string().min(1),officer_id: z.string().min(1),territory_id: z.uuid(),point,distance_m: z.number().nonnegative(),recorded_at: z.iso.datetime({offset: true})});
export const recordFieldVisit = (territoryID: string, location: z.infer<typeof point>, key: string, csrf: string, signal?: AbortSignal) => requestJSON(`/admin/api/v1/fulfillment/territories/${z.uuid().parse(territoryID)}/field-check-ins`, visit, {method: "POST",headers: {"Content-Type":"application/json","X-CSRF-Token":csrf,"Idempotency-Key":key},body: JSON.stringify({point: point.parse(location)}),signal});
