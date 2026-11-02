import {z} from "zod";
import {requestJSON} from "./client";

const uuid=z.uuid();
const group=z.object({id:uuid,node_id:uuid,name:z.string().min(1).max(128),role_ids:z.array(uuid).min(1).max(32),include_descendants:z.boolean(),active:z.boolean(),revision:z.number().int().positive()});
export type AudienceGroup=z.infer<typeof group>;
export type AudienceInput=Omit<AudienceGroup,"id"> & {id?:string};
export const getAudienceGroups=(signal?:AbortSignal,after?:string)=>requestJSON("/admin/api/v1/organization/audiences"+(after ? `?after=${encodeURIComponent(uuid.parse(after))}` : ""),z.object({groups:z.array(group).max(100),next_cursor:uuid.optional()}),{signal});
export function saveAudienceGroup(value:AudienceInput,csrf:string,signal?:AbortSignal){
 const {id,...definition}=value;
 return requestJSON(id ? `/admin/api/v1/organization/audiences/${encodeURIComponent(uuid.parse(id))}` : "/admin/api/v1/organization/audiences",group,{method:id ? "PUT" : "POST",headers:{"Content-Type":"application/json","X-CSRF-Token":csrf},body:JSON.stringify(definition),signal});
}
export const previewAudience=(id:string,subject:string,csrf:string,signal?:AbortSignal)=>requestJSON(`/admin/api/v1/organization/audiences/${encodeURIComponent(uuid.parse(id))}/preview`,z.object({member:z.boolean()}),{method:"POST",headers:{"Content-Type":"application/json","X-CSRF-Token":csrf},body:JSON.stringify({subject_id:uuid.parse(subject)}),signal});
