import {useEffect,useId,useRef,useState,type SyntheticEvent} from "react";
import {useQueryClient} from "@tanstack/react-query";
import {APIError,type AdminSession} from "../api/client";
import {previewAudience,saveAudienceGroup,type AudienceGroup} from "../api/audiences";
import {useAudienceDirectory} from "../lib/audience-directory";
import type {OrganizationWorkspace} from "../api/organization";
import {Button} from "../components/button";
import {presentOrganizationAction} from "../lib/organization-presentation";
import {t} from "../lib/i18n";

export function AudienceWorkspace({session,workspace}:{session:AdminSession;workspace:OrganizationWorkspace}){
 const client=useQueryClient();
 const [selected,setSelected]=useState("");
 const [savedSelection,setSavedSelection]=useState<string|null>(null);
 const {query,groups}=useAudienceDirectory(session);
 const group=groups.find(value=>value.id===selected);
 useEffect(()=>{
  if(!savedSelection || !query.isSuccess || query.isFetching)return;
  if(groups.some(value=>value.id===savedSelection) || !query.hasNextPage){setSavedSelection(null);return;}
  void query.fetchNextPage();
 },[savedSelection,groups,query]);
 const nodes=workspace.nodes.filter(node=>node.active && presentOrganizationAction(session,workspace,node.id,"staff.manage"));
 const canEdit=group ? nodes.some(node=>node.id===group.node_id) : nodes.length>0;
 const scopeVersion=workspace.nodes.map(node=>`${node.id}:${String(node.active)}:${String(node.revision)}`).join("|")+workspace.staff.map(staff=>`${staff.subject_id}:${staff.node_id}:${staff.role_id}:${staff.status}:${String(staff.revision)}`).join("|");
 return <section className="surface-card audience-workspace" aria-label={t("audienceTitle")}>
  <h2>{t("audienceTitle")}</h2><p>{t("audienceHelp")}</p>
  <Button variant="secondary" disabled={query.isFetching} onClick={()=>{void query.refetch();void client.invalidateQueries({queryKey:["admin","organization"]});}}>{t("audienceRefresh")}</Button>
  {query.isPending && <p role="status">{t("loading")}</p>}
  {query.isError && <p role="alert">{t("audienceFailed")}</p>}
  {query.isSuccess && <>
   {groups.length===0 && <p role="status">{t("audienceEmpty")}</p>}
   <ul>{groups.map(item=><li key={item.id}><Button variant="secondary" aria-pressed={item.id===selected} onClick={()=>{setSelected(item.id);}}>{item.name}</Button> · {item.active ? t("operatorActive") : t("operatorSuspended")} · {t("audienceRevision")} {item.revision}</li>)}</ul>
   {query.hasNextPage && <Button variant="secondary" disabled={query.isFetching} onClick={()=>{void query.fetchNextPage();}}>{t("audienceMore")}</Button>}
   {nodes.length>0 && <Button variant="secondary" onClick={()=>{setSelected("");}}>{t("audienceNew")}</Button>}
   {group && !canEdit && <p>{t("audienceReadOnly")}</p>}
   {canEdit && (selected==="" || group!==undefined) && <AudienceEditor key={`${session.subject_id}:${session.selected_country}:${group?.id??"new"}:${String(group?.revision??0)}:${scopeVersion}`} session={session} workspace={workspace} nodes={nodes} group={group} scopeVersion={scopeVersion} onSaved={id=>{setSelected(id);setSavedSelection(id);void query.refetch();}} onDenied={()=>{void query.refetch();}} />}
  </>}
 </section>;
}

function AudienceEditor({session,workspace,nodes,group,scopeVersion,onSaved,onDenied}:{session:AdminSession;workspace:OrganizationWorkspace;nodes:OrganizationWorkspace["nodes"];group:AudienceGroup|undefined;scopeVersion:string;onSaved:(id:string)=>void;onDenied:()=>void}){
 const inputID=useId();
 const client=useQueryClient();
 const [name,setName]=useState(group?.name??"");const [node,setNode]=useState(group?.node_id??nodes[0]?.id??"");
 const [roles,setRoles]=useState<string[]>(group?.role_ids??[]);const [descendants,setDescendants]=useState(group?.include_descendants??true);const [active,setActive]=useState(group?.active??true);
 const [subject,setSubject]=useState("");const [preview,setPreview]=useState<{subject:string;scope:string;member:boolean}|null>(null);
 const [pending,setPending]=useState(false);const [blocked,setBlocked]=useState(false);const [message,setMessage]=useState("");
 const mounted=useRef(false);const busy=useRef(false);const request=useRef<AbortController|null>(null);
 useEffect(()=>{mounted.current=true;return()=>{mounted.current=false;request.current?.abort();};},[]);
 const allowed=session.assurance.fresh_auth && !blocked;
 const subjects=[...new Set(workspace.staff.map(value=>value.subject_id))];
 const savedCriteria=name===group?.name && node===group.node_id && descendants===group.include_descendants && active===group.active && [...roles].sort().join(",")===[...group.role_ids].sort().join(",");
 const run=async(operation:(signal:AbortSignal)=>Promise<void>)=>{
  if(busy.current || !allowed)return;busy.current=true;setPending(true);setMessage("");setPreview(null);const abort=new AbortController();request.current=abort;
  try{await operation(abort.signal);}catch(error){if(!mounted.current)return;setMessage(t(error instanceof APIError && error.status===409 ? "audienceConflict" : "failed"));if(error instanceof APIError && [401,403,404].includes(error.status)){setBlocked(true);onDenied();void client.invalidateQueries({queryKey:["admin","organization"]});}}
  finally{busy.current=false;if(mounted.current)setPending(false);}
 };
 const save=(event:SyntheticEvent<HTMLFormElement>)=>{event.preventDefault();void run(async signal=>{const result=await saveAudienceGroup({...(group?{id:group.id}:{}),node_id:node,name:name.trim(),role_ids:roles,include_descendants:descendants,active,revision:group?.revision??0},session.csrf_token,signal);if(mounted.current){setMessage(t("audienceSaved"));onSaved(result.id);}});};
 const check=(event:SyntheticEvent<HTMLFormElement>)=>{event.preventDefault();if(!group || !savedCriteria || !subject)return;void run(async signal=>{const result=await previewAudience(group.id,subject,session.csrf_token,signal);if(mounted.current)setPreview({subject,scope:scopeVersion,member:result.member});});};
 return <div className="audience-editor">
  <h3>{group ? t("audienceEdit") : t("audienceNew")}</h3>
  {!session.assurance.fresh_auth && <p><a href="/login?reauth=mfa">{t("fresh")}</a></p>}
  {message && <p role="status">{message}</p>}
  <form onSubmit={save}><fieldset disabled={pending || !allowed}><legend>{t("audienceCriteria")}</legend>
   <div className="audience-field"><label htmlFor={inputID+"-name"}>{t("audienceName")}</label><input id={inputID+"-name"} value={name} onChange={event=>{setName(event.target.value);}} required maxLength={128} /></div>
   <div className="audience-field"><label htmlFor={inputID+"-territory"}>{t("territory")}</label><select id={inputID+"-territory"} value={node} onChange={event=>{setNode(event.target.value);}} required>{nodes.map(item=><option key={item.id} value={item.id}>{item.name}</option>)}</select></div>
   <fieldset><legend>{t("audienceRoles")}</legend>{workspace.roles.map(role=><label key={role.id}><input type="checkbox" checked={roles.includes(role.id)} onChange={event=>{setRoles(event.target.checked ? [...roles,role.id] : roles.filter(value=>value!==role.id));}} /> {role.name}</label>)}</fieldset>
   <label><input type="checkbox" checked={descendants} onChange={event=>{setDescendants(event.target.checked);}} /> {t("audienceDescendants")}</label>
   <label><input type="checkbox" checked={active} onChange={event=>{setActive(event.target.checked);}} /> {t("audienceActive")}</label>
   <Button disabled={roles.length===0 || !name.trim()}>{pending ? t("audienceSaving") : t("audienceSave")}</Button>
  </fieldset></form>
  {group && <form onSubmit={check}><fieldset disabled={pending || !allowed || !savedCriteria}><legend>{t("audiencePreview")}</legend>
   {!savedCriteria && <p>{t("audienceSaveBeforePreview")}</p>}
   <div className="audience-field"><label htmlFor={inputID+"-participant"}>{t("audienceParticipant")}</label><select id={inputID+"-participant"} value={subject} onChange={event=>{setSubject(event.target.value);}} required><option value="">{t("select")}</option>{subjects.map(id=><option key={id} value={id}>{id}</option>)}</select></div>
   <Button variant="secondary" disabled={!subject}>{t("audienceCheck")}</Button>
   {savedCriteria && preview?.subject===subject && preview.scope===scopeVersion && <p role="status">{t(preview.member ? "audienceMember" : "audienceNotMember")}</p>}
  </fieldset></form>}
 </div>;
}
