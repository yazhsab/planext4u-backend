import {useEffect,useRef,useState} from "react";
import type {AdminSession} from "../api/client";
import {getRegionalSummaryCSV} from "../api/fulfillment";
import {Button} from "../components/button";
import {t} from "../lib/i18n";

export function RegionalSummaryExport({session,region,onDenied}: {session:AdminSession;region:string;onDenied:()=>void}) {
 const [busy,setBusy]=useState(false),[ready,setReady]=useState<{scope:string;url:string}>(),[failed,setFailed]=useState(false);
 const scope=JSON.stringify([session.subject_id,session.selected_country,region,session.capabilities.includes("admin.fulfillment.export"),session.assurance.fresh_auth]);
 const currentScope=useRef(scope);currentScope.current=scope;
 const active=useRef(false),pending=useRef<AbortController|undefined>(undefined),url=useRef<string|undefined>(undefined);
 const clear=()=>{if(url.current)URL.revokeObjectURL(url.current);url.current=undefined;};
 useEffect(()=>{active.current=true;return()=>{active.current=false;pending.current?.abort();clear();};},[scope]);
 if(!session.capabilities.includes("admin.fulfillment.export"))return null;
 const prepare=async()=>{
  pending.current?.abort();const operation=new AbortController();pending.current=operation;
  clear();setReady(undefined);setBusy(true);setFailed(false);
  try{
   const blob=await getRegionalSummaryCSV(region,operation.signal);
   if(!active.current || pending.current!==operation || operation.signal.aborted || currentScope.current!==scope)return;
   url.current=URL.createObjectURL(blob);setReady({scope,url:url.current});
  }catch{
   if(!active.current || pending.current!==operation || operation.signal.aborted)return;
   setFailed(true);onDenied();
  }finally{if(active.current && pending.current===operation)setBusy(false);}
 };
 return <div className="report-export-control"><p>{t("deliveryExportHelp")}</p>
  <Button variant="secondary" disabled={busy || !session.assurance.fresh_auth} onClick={()=>{void prepare();}}>{t(busy?"deliveryExportPreparing":"deliveryExport")}</Button>
  {!session.assurance.fresh_auth && <a href="/login?reauth=mfa">{t("deliveryExportReauth")}</a>}
  {ready?.scope===scope && <a href={ready.url} download="delivery-summary.csv">{t("deliveryExportDownload")}</a>}
  {failed && <p role="alert">{t("deliveryExportFailed")}</p>}
 </div>;
}
