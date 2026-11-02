import { Button } from "../components/button";
import { useState, type SyntheticEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useOutletContext } from "react-router";
import { AlertTriangle } from "lucide-react";
import { type AdminSession } from "../api/client";
import { acceptInvitation, bindTerritory, createJobRole, createNode, getOrganization, getTerritoryBindings, inviteStaff, setNodeStatus, setStaff, type OrganizationWorkspace } from "../api/organization";
import { StatePanel } from "../components/state-panel";
import { t } from "../lib/i18n";
import {FulfillmentWorkspace} from "./fulfillment-workspace";
import {presentOrganizationAction} from "../lib/organization-presentation";
import {AudienceWorkspace} from "./audience-workspace";

const permissions = ["organization.read", "organization.manage", "staff.manage", "resource.read", "resource.export", "resource.command"];
const permissionLabel = (permission: string) => ({"organization.read": t("viewOrganization"), "organization.manage": t("manageOrganization"), "staff.manage": t("manageStaff"), "resource.read": t("viewResources"), "resource.export": t("exportResources"), "resource.command": t("changeResources")})[permission] ?? permission;
export function OrganizationPage() {
  const session = useOutletContext<AdminSession>();
  const client = useQueryClient();
  const [search, setSearch] = useState("");
  const [message, setMessage] = useState("");
  const [roleNode,setRoleNode] = useState("");
  const [staffNode,setStaffNode] = useState("");
  const query = useQuery({queryKey: ["admin", "organization", session.selected_country, search], queryFn: ({signal}) => getOrganization(search, signal), retry: false});
  const mutation = useMutation({mutationFn: async (action: () => Promise<unknown>) => action(), onSuccess: async (result) => { setMessage(typeof result === "object" && result !== null && "token" in result && typeof result.token === "string" ? `${t("inviteHelp")} ${result.token}` : t("saved")); await client.invalidateQueries({queryKey: ["admin", "organization"]}); }, onError: () => { setMessage(t("failed")); }});
  const execute = (event: SyntheticEvent<HTMLFormElement>, action: (form: FormData) => Promise<unknown>) => { event.preventDefault(); const data = new FormData(event.currentTarget); mutation.mutate(() => action(data)); };
  const text = (form: FormData, key: string) => { const value = form.get(key); return typeof value === "string" ? value.trim() : ""; };
  const csrf = session.csrf_token;
  const busy = mutation.isPending || !session.assurance.fresh_auth;
  const allowed = (node: string,capability: string) => query.data !== undefined && presentOrganizationAction(session,query.data,node,capability);
  const manageNodes = query.data?.nodes.filter((node) => node.active && allowed(node.id,"organization.manage")) ?? [];
  const staffNodes = query.data?.nodes.filter((node) => node.active && allowed(node.id,"staff.manage")) ?? [];
  const choices = (nodes: OrganizationWorkspace["nodes"]) => nodes.map((node) => <option key={node.id} value={node.id}>{node.name}</option>);
  const invitationForm = <section className="surface-card"><h2>{t("accept")}</h2><form onSubmit={(event) => { event.preventDefault(); const token = text(new FormData(event.currentTarget), "token"); event.currentTarget.reset(); mutation.mutate(() => acceptInvitation(token, csrf)); }}>
    <label>{t("invitation")}<input name="token" type="password" autoComplete="off" required minLength={43} maxLength={43} pattern="[A-Za-z0-9_-]{43}" /></label><Button disabled={busy}>{t("accept")}</Button>
  </form></section>;
  if (query.isPending) return <p role="status">{t("loading")}</p>;
  if (query.isError) return <div className="page-stack organization-workspace">{session.capabilities.includes("admin.fulfillment.read") && <FulfillmentWorkspace session={session} />}{message && <p role="status">{message}</p>}{invitationForm}<StatePanel icon={AlertTriangle} title={t("failed")} message={query.error.message} actionLabel={t("retry")} onAction={() => { void query.refetch(); }} /></div>;
  return <div className="page-stack organization-workspace">
    <header className="page-header"><div><h1>{t("organizationTitle")}</h1><p>{t("organizationHelp")}</p></div></header>
    {session.capabilities.includes("admin.fulfillment.read") && <FulfillmentWorkspace session={session} />}
    {!session.assurance.fresh_auth && <p role="status">{t("fresh")}</p>}
    {message && <p role="status">{message}</p>}
    <label>{t("search")}<input value={search} onChange={(event) => { setSearch(event.target.value); }} /></label>
    <AudienceWorkspace key={session.subject_id+":"+session.selected_country} session={session} workspace={query.data} />
    <section className="surface-card"><h2>{t("nodes")}</h2>
      {query.data.nodes.length === 0 && <p>{t("empty")}</p>}
      <ul>{query.data.nodes.map((node) => <li key={node.id}>{node.name} · {node.kind} · {node.active ? "ACTIVE" : "INACTIVE"} <small>{node.parent_id}</small>{session.roles.includes("SUPER_ADMIN") && <Button disabled={busy} onClick={() => { mutation.mutate(() => setNodeStatus(node.id, node.revision, !node.active, csrf)); }}>{t(node.active ? "deactivateTerritory" : "activateTerritory")}</Button>}</li>)}</ul>
      {(session.roles.includes("SUPER_ADMIN") || manageNodes.length > 0) && <form onSubmit={(event) => { execute(event, (form) => createNode({name: text(form, "name"), parent_id: text(form, "parent"), kind: text(form, "kind")}, csrf)); }}>
        <label>{t("name")}<input name="name" required maxLength={128} /></label>
        <label>{t("parent")}<select name="parent" required={!session.roles.includes("SUPER_ADMIN")}><option value="">{session.roles.includes("SUPER_ADMIN") ? t("root") : t("select")}</option>{choices(manageNodes)}</select></label>
        <label>{t("kind")}<select name="kind"><option value="TERRITORY">{t("territory")}</option><option value="ORGANIZATION">{t("organization")}</option></select></label>
        <Button disabled={busy}>{t("create")}</Button>
      </form>}
    </section>
    <section className="surface-card"><h2>{t("roles")}</h2><ul>{query.data.roles.map((role) => <li key={role.id}>{role.name} · {role.capabilities.map(permissionLabel).join(", ")}</li>)}</ul>
      {staffNodes.length > 0 && <form onSubmit={(event) => { execute(event, (form) => createJobRole({name: text(form, "name"), node_id: text(form, "node"), capabilities: form.getAll("capability").filter((value): value is string => typeof value === "string")}, csrf)); }}>
        <label>{t("name")}<input name="name" required maxLength={128} /></label><label>{t("territory")}<select name="node" required value={roleNode} onChange={(event) => {setRoleNode(event.target.value);}}><option value="">{t("select")}</option>{choices(staffNodes)}</select></label>
        <fieldset key={roleNode}><legend>{t("capabilities")}</legend>{roleNode === "" && <p>{t("choosePermissionTerritory")}</p>}{permissions.filter((permission) => roleNode !== "" && allowed(roleNode,permission)).map((permission) => <label key={permission}><input type="checkbox" name="capability" value={permission} />{permissionLabel(permission)}</label>)}</fieldset><Button disabled={busy}>{t("create")}</Button>
      </form>}
    </section>
    <section className="surface-card"><h2>{t("staff")}</h2>
      <ul>{query.data.staff.map((staff) => <li key={`${staff.subject_id}:${staff.node_id}:${staff.role_id}`}>{staff.subject_id} · {query.data.nodes.find((node) => node.id === staff.node_id)?.name} · {staff.status} {allowed(staff.node_id,"staff.manage") && query.data.roles.some((role) => role.id === staff.role_id && role.capabilities.every((capability) => allowed(staff.node_id,capability))) && <Button disabled={busy} onClick={() => { mutation.mutate(() => setStaff({...staff, status: staff.status === "ACTIVE" ? "SUSPENDED" : "ACTIVE"}, csrf)); }}>{staff.status === "ACTIVE" ? "Suspend" : "Activate"}</Button>}</li>)}</ul>
      {staffNodes.length > 0 && <form onSubmit={(event) => { execute(event, (form) => inviteStaff({subject_id: text(form, "subject"), node_id: text(form, "node"), role_id: text(form, "role")}, csrf)); }}>
        <label>{t("subject")}<input name="subject" required autoComplete="off" /></label><label>{t("territory")}<select name="node" required value={staffNode} onChange={(event) => {setStaffNode(event.target.value);}}><option value="">{t("select")}</option>{choices(staffNodes)}</select></label>
        <label>{t("roles")}<select key={staffNode} name="role" required><option value="">{t("select")}</option>{query.data.roles.filter((role) => staffNode !== "" && role.capabilities.every((capability) => allowed(staffNode,capability))).map((role) => <option key={role.id} value={role.id}>{role.name}</option>)}</select></label><Button disabled={busy}>{t("invite")}</Button>
      </form>}
    </section>
    {session.roles.includes("SUPER_ADMIN") && <TerritoryBindings session={session} nodes={query.data.nodes} />}
    {invitationForm}
  </div>;
}

function TerritoryBindings({session, nodes}: {session: AdminSession; nodes: OrganizationWorkspace["nodes"]}) {
  const client = useQueryClient();
  const query = useQuery({queryKey: ["admin", "organization", "territory-bindings", session.selected_country], queryFn: ({signal}) => getTerritoryBindings(signal), retry: false});
  const [message, setMessage] = useState("");
  const mutation = useMutation({mutationFn: async ({id, node, revision, reason}: {id: string; node: string; revision: number; reason: string}) => bindTerritory(id, {node_id: node, revision, reason}, session.csrf_token),
    onSuccess: async () => {setMessage("Territory access link saved."); await client.invalidateQueries({queryKey: ["admin", "organization", "territory-bindings"]});},
    onError: () => {setMessage("The link was not saved. Refresh the list and check your permissions and revision.");}});
  return <section className="surface-card territory-binding-workspace"><h2>Fulfillment territory access</h2>
    <p>Link an existing delivery territory to its organization territory. Active staff inherit their assigned resource permissions. Suspending a staff assignment or an ancestor removes delegated access. This does not grant country-wide finance or other-person attendance access.</p>
    {message && <p role="status">{message}</p>}
    {query.isPending && <p role="status">Loading delivery territories…</p>}
    {query.isError && <p role="alert">Delivery territories could not be loaded.</p>}
    <Button disabled={mutation.isPending} onClick={() => {void query.refetch();}}>Refresh territory links</Button>
    {query.data?.items.map((territory) => <form key={`${territory.territory_id}:${territory.revision.toString()}`} onSubmit={(event) => {
      event.preventDefault(); const data = new FormData(event.currentTarget);
      const node = data.get("node"); const reason = data.get("reason");
      if (typeof node !== "string" || typeof reason !== "string") return;
      mutation.mutate({id: territory.territory_id, node, revision: territory.revision, reason});
    }}>
      <h3>{territory.name} · {territory.region_id}</h3>
      <div className="territory-binding-field"><label htmlFor={`territory-node-${territory.territory_id}`}>Organization territory for {territory.name}</label><select id={`territory-node-${territory.territory_id}`} name="node" required defaultValue={territory.node_id ?? ""}>
        <option value="">Select territory</option>{nodes.filter((node) => node.active && node.kind === "TERRITORY").map((node) => <option key={node.id} value={node.id}>{node.name}</option>)}
      </select></div>
      <div className="territory-binding-field"><label htmlFor={`territory-reason-${territory.territory_id}`}>Reason for linking {territory.name}</label><input id={`territory-reason-${territory.territory_id}`} name="reason" required minLength={8} maxLength={500} /></div>
      <Button disabled={mutation.isPending || !session.assurance.fresh_auth}>Save territory link</Button>
    </form>)}
  </section>;
}
