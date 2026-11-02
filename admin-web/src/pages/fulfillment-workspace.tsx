import {useState} from "react";
import {useQuery} from "@tanstack/react-query";
import type {AdminSession} from "../api/client";
import {getFieldTerritories,getFulfillmentDashboard, getFulfillmentTerritories} from "../api/fulfillment";
import {Button} from "../components/button";
import {t} from "../lib/i18n";
import {FieldCheckIn} from "./field-check-in";
import {RegionalSummaryExport} from "./regional-summary-export";

export function FulfillmentWorkspace({session}: {session: AdminSession}) {
  const [region, setRegion] = useState("");
  const [exportEpoch,setExportEpoch]=useState(0);
  const scope = ["admin", "fulfillment", session.subject_id, session.selected_country];
  const territories = useQuery({queryKey: [...scope, "territories"],queryFn: ({signal}) => getFulfillmentTerritories(signal),retry: false});
  const items = territories.isSuccess ? territories.data.items : [];
  const regions = [...new Set(items.map((item) => item.region_id))];
  const selected = regions.includes(region) ? region : regions[0] ?? "";
  const dashboard = useQuery({queryKey: [...scope,"dashboard",selected],queryFn: ({signal}) => getFulfillmentDashboard(selected,signal),enabled: selected !== "",retry: false});
  const fieldEnabled=session.capabilities.includes("admin.fulfillment.command");
  const fieldScope=useQuery({queryKey:[...scope,"field-scope"],queryFn:({signal})=>getFieldTerritories(signal),enabled:fieldEnabled,retry:false});
  const fieldTerritories=fieldScope.isSuccess ? items.filter((item)=>fieldScope.data.territory_ids.includes(item.id)) : [];
  return <section className="surface-card delivery-workspace" aria-label={t("deliveryTitle")}>
    <h2>{t("deliveryTitle")}</h2><p>{t("deliveryHelp")}</p>
    <Button disabled={territories.isFetching || dashboard.isFetching || fieldScope.isFetching} onClick={() => {setExportEpoch(value=>value+1);void territories.refetch(); if (selected) void dashboard.refetch(); if(fieldEnabled) void fieldScope.refetch();}}>{t("deliveryRefresh")}</Button>
    {territories.isPending && <p role="status">{t("loading")}</p>}
    {territories.isError && <p role="alert">{t("deliveryFailed")}</p>}
    {territories.isSuccess && items.length === 0 && <p role="status">{t("deliveryEmpty")}</p>}
    {fieldEnabled && fieldScope.isError && <p role="alert">{t("fieldScopeFailed")}</p>}
    {fieldTerritories.length>0 && <FieldCheckIn key={session.subject_id+":"+session.selected_country+":"+fieldTerritories.map((item)=>item.id).sort().join(",")} session={session} territories={fieldTerritories} onRecorded={()=>{void dashboard.refetch();}} onDenied={()=>{void fieldScope.refetch();void territories.refetch();}} />}
    {items.length > 0 && <>
      <ul>{items.map((item) => <li key={item.id}>{item.name} · {item.region_id}</li>)}</ul>
      <label htmlFor="delivery-region">{t("deliveryRegion")}</label>
      <select id="delivery-region" value={selected} onChange={(event) => {setRegion(event.target.value);}}>{regions.map((item) => <option key={item} value={item}>{item}</option>)}</select>
      {dashboard.isPending && <p role="status">{t("loading")}</p>}
      {dashboard.isError && <p role="alert">{t("deliveryFailed")}</p>}
      {dashboard.isSuccess && dashboard.data.region_id === selected && <div aria-live="polite"><dl className="delivery-metrics">
        <div><dt>{t("deliveryTerritories")}</dt><dd>{dashboard.data.territories}</dd></div>
        <div><dt>{t("deliveryRiders")}</dt><dd>{dashboard.data.online_riders}</dd></div>
        <div><dt>{t("deliveryUnassigned")}</dt><dd>{dashboard.data.unassigned_tasks}</dd></div>
        <div><dt>{t("deliveryAtRisk")}</dt><dd>{dashboard.data.at_risk_tasks}</dd></div>
        <div><dt>{t("deliveryCompleted")}</dt><dd>{dashboard.data.completed_today}</dd></div>
      </dl><p>{t("deliveryUpdated")} <time dateTime={dashboard.data.generated_at}>{new Date(dashboard.data.generated_at).toLocaleString()}</time></p></div>}
      {dashboard.isSuccess && <RegionalSummaryExport key={session.subject_id+":"+session.selected_country+":"+selected+":"+exportEpoch.toString()} session={session} region={selected} onDenied={()=>{void territories.refetch();void dashboard.refetch();}} />}
    </>}
  </section>;
}
