import {useEffect, useState, type SyntheticEvent} from "react";
import {useQueryClient} from "@tanstack/react-query";
import {AlertTriangle, LockKeyhole, ShieldCheck} from "lucide-react";
import {useOutletContext} from "react-router";

import {APIError, type AdminSession} from "../api/client";
import {getPrivacyDeletion, getPrivacyProposal, listPrivacyDeletions, listPrivacyHolds, listPrivacyProposals, placePrivacyHold, proposePrivacyRetention, publishPrivacyRetention, releasePrivacyHold, type PrivacyDeletion, type PrivacyHold, type PrivacyProposal} from "../api/privacy";
import {Button} from "../components/button";
import {StatePanel} from "../components/state-panel";

const ownerPattern = /^[a-z_]{1,128}$/;
const versionPattern = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/;
const reasonValid = (value: string) => value.trim().length >= 8 && value.trim().length <= 256;
const when = (value: string) => new Date(value).toLocaleString();

export function PrivacyPage() {
  const session = useOutletContext<AdminSession>();
  const country = session.roles.includes("COUNTRY_ADMIN") || session.roles.includes("SUPER_ADMIN");
  const policy = session.capabilities.includes("admin.policy.manage");
  const fresh = session.assurance.fresh_auth && session.assurance.mfa_satisfied;
  if (!country || !policy) return <StatePanel icon={LockKeyhole} title="Privacy governance access unavailable" message="Current country administrator policy authority is required." />;
  if (!fresh) return <section className="surface-card"><h1>Privacy governance requires fresh MFA</h1><p>Sign in again before viewing deletion requests or recording decisions.</p><Button asChild><a href="/login?reauth=mfa">Verify administrator identity</a></Button></section>;
  return <PrivacyWorkspace key={`${session.subject_id}:${session.selected_country}:${session.assurance.auth_time}`} session={session} />;
}

function PrivacyWorkspace({session}: {session: AdminSession}) {
  const client = useQueryClient();
  const [reload, setReload] = useState(0);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState("");
  const [proposals, setProposals] = useState<PrivacyProposal[]>([]);
  const [deletions, setDeletions] = useState<PrivacyDeletion[]>([]);
  const [holds, setHolds] = useState<PrivacyHold[]>([]);
  const [pending, setPending] = useState(false);
  const [blocked, setBlocked] = useState(false);
  const [message, setMessage] = useState("");
  const [commandError, setCommandError] = useState("");
  const [proposalID, setProposalID] = useState("");
  const [holdID, setHoldID] = useState<number | null>(null);
  const [requestID, setRequestID] = useState("");
  const [owner, setOwner] = useState("");
  const [version, setVersion] = useState("");
  const [days, setDays] = useState("");
  const [revision, setRevision] = useState("0");
  const [proposalReason, setProposalReason] = useState("");
  const [holdOwner, setHoldOwner] = useState("");
  const [holdReason, setHoldReason] = useState("");
  const [releaseReason, setReleaseReason] = useState("");
  const [draftID, setDraftID] = useState(() => crypto.randomUUID());

  useEffect(() => {
    const abort = new AbortController();
    setLoading(true); setLoadError(""); setProposals([]); setDeletions([]); setHolds([]);
    void Promise.all([listPrivacyProposals(abort.signal), listPrivacyDeletions(abort.signal), listPrivacyHolds(abort.signal)])
      .then(([p,d,h]) => {if (!abort.signal.aborted) {setProposals(p.items); setDeletions(d.items); setHolds(h.items); setLoading(false);}})
      .catch((error: unknown) => {if (!abort.signal.aborted) {setLoading(false); setLoadError(error instanceof APIError ? `${error.message} Reference: ${error.correlationID}` : "The scoped privacy register could not be loaded."); if (error instanceof APIError && [401,403].includes(error.status)) {setBlocked(true); void client.invalidateQueries({queryKey:["admin","session"]});}}});
    return () => {abort.abort();};
  }, [reload, client]);

  const execute = async (operation: () => Promise<unknown>, success: string) => {
    if (pending || blocked) return;
    setPending(true); setMessage(""); setCommandError("");
    try {await operation(); setMessage(success); setProposalID(""); setHoldID(null); setRequestID(""); setReload(value => value + 1); return true;}
    catch (error) {
      if (error instanceof APIError && [401,403].includes(error.status)) {setBlocked(true); setProposals([]); setDeletions([]); setHolds([]); void client.invalidateQueries({queryKey:["admin","session"]});}
      setCommandError(error instanceof APIError ? `${error.message} Reference: ${error.correlationID}` : "The privacy decision could not be recorded. Check the fields and retry.");
      return false;
    } finally {setPending(false);}
  };
  const selectedProposal = proposals.find(value => value.id === proposalID);
  const selectedHold = holds.find(value => value.id === holdID);
  const selectedRequest = deletions.find(value => value.id === requestID);
  const proposedRevision = Number(revision), proposedDays = Number(days);
  const proposalValid = ownerPattern.test(owner) && versionPattern.test(version) && days !== "" && Number.isSafeInteger(proposedDays) && proposedDays >= 0 && proposedDays <= 36500 && Number.isSafeInteger(proposedRevision) && proposedRevision >= 0 && reasonValid(proposalReason);
  const holdValid = selectedRequest?.status === "SCHEDULED" && ownerPattern.test(holdOwner) && reasonValid(holdReason);

  return <div className="page-stack privacy-workspace">
    <header className="page-header"><div><p className="eyebrow">Country control plane · {session.selected_country}</p><h1>Privacy governance</h1><p>Review retention proposals, scheduled deletion requests and active legal holds for the selected country.</p></div><ShieldCheck aria-hidden="true" /></header>
    <p className="page-intro">A different country administrator must publish a retention proposal or release a hold placed by another administrator. These decisions are recorded by the owning identity service. A published policy does not itself complete erasure.</p>
    {blocked && <section className="surface-card" role="alert"><h2>Current authority expired</h2><p>Sign in again with MFA to continue. This page has cleared the scoped records.</p><Button asChild><a href="/login?reauth=mfa">Verify administrator identity</a></Button></section>}
    {message && !blocked && <p role="status">{message}</p>}
    {commandError && !blocked && <p role="alert">{commandError}</p>}
    {!blocked && <Button variant="secondary" disabled={loading || pending} onClick={() => {setReload(value => value + 1);}}>Refresh current country</Button>}
    {loading && !blocked && <p role="status">Loading scoped privacy register…</p>}
    {loadError && !blocked && <StatePanel icon={AlertTriangle} tone="danger" title="Privacy register unavailable" message={loadError} actionLabel="Try again" onAction={() => {setReload(value => value + 1);}} />}
    {!loading && !loadError && !blocked && <>
      <section className="surface-card"><h2>Retention proposals</h2><p>Only an independently approved, published version governs retention. Enter the current revision when proposing a new version.</p>
        {proposals.length === 0 ? <p>No proposals in this country.</p> : <ul className="privacy-list">{proposals.map(item => <li key={item.id}><strong>{item.owner} · {item.policy_version}</strong><span>{item.status} · revision {item.expected_revision} → {item.expected_revision + 1} · {Math.round(item.retention_microseconds / 86_400_000_000)} days</span><small>Requested {when(item.requested_at)} · by {item.requested_by} · reference {item.reason_reference}</small>{item.status === "PENDING" && <Button variant="secondary" disabled={pending || item.requested_by === session.subject_id} onClick={() => {setProposalID(item.id); setHoldID(null);}}>{item.requested_by === session.subject_id ? "Another administrator must publish" : `Review publication: ${item.owner}`}</Button>}</li>)}</ul>}
        {selectedProposal?.status === "PENDING" && selectedProposal.requested_by !== session.subject_id && <div className="privacy-decision"><h3>Confirm retention publication</h3><p>{selectedProposal.owner} · {selectedProposal.policy_version} · requested revision {selectedProposal.expected_revision} · reference {selectedProposal.reason_reference}</p><p>New deletion decisions will use this version after the owning service accepts publication.</p><Button disabled={pending} onClick={() => {void execute(async () => {const current=await getPrivacyProposal(selectedProposal.id); if(current.status!=="PENDING" || current.requested_by===session.subject_id || current.expected_revision!==selectedProposal.expected_revision) throw new Error("The proposal changed. Refresh before publishing."); await publishPrivacyRetention(current.id,session.csrf_token);},"Retention policy published for this country.");}}>Publish reviewed proposal</Button><Button variant="secondary" disabled={pending} onClick={() => {setProposalID("");}}>Cancel</Button></div>}
        <form onSubmit={(event: SyntheticEvent<HTMLFormElement>) => {event.preventDefault(); if (!proposalValid) return; const input={id:draftID,owner,policy_version:version,retention_seconds:proposedDays*86400,expected_revision:proposedRevision,reason_reference:proposalReason.trim()}; void execute(() => proposePrivacyRetention(input,session.csrf_token),"Retention proposal recorded. An independent administrator must publish it.").then(ok => {if(ok) setDraftID(crypto.randomUUID());});}}><fieldset disabled={pending}><legend>Propose a retention version</legend><label>Data owner<input required pattern="[a-z_]+" maxLength={128} value={owner} onChange={event => {setOwner(event.target.value);}} placeholder="identity" autoComplete="off" /></label><label>Policy version<input required maxLength={128} value={version} onChange={event => {setVersion(event.target.value);}} placeholder="approved-reference-v1" autoComplete="off" /></label><label>Retention days<input required type="number" min="0" max="36500" step="1" value={days} onChange={event => {setDays(event.target.value);}} /></label><label>Current revision<input required type="number" min="0" step="1" value={revision} onChange={event => {setRevision(event.target.value);}} /></label><label>Decision reference<input required minLength={8} maxLength={256} value={proposalReason} onChange={event => {setProposalReason(event.target.value);}} placeholder="Approved policy or legal reference" /></label><Button disabled={!proposalValid || pending}>Record proposal</Button></fieldset></form>
      </section>
      <section className="surface-card"><h2>Scheduled deletion requests</h2><p>The latest 100 scheduled requests for {session.selected_country}; identity IDs are shown only within this fresh MFA session.</p>{deletions.length === 0 ? <p>No scheduled deletion requests.</p> : <ul className="privacy-list">{deletions.map(item => <li key={item.id}><strong>Request {item.id}</strong><span>Identity {item.identity_id} · effective {when(item.effective_at)}</span><small>Owner tasks ready {item.ready_owners} · blocked {item.blocked_owners} · active holds {item.active_holds}</small><Button variant="secondary" disabled={pending} onClick={() => {setRequestID(item.id); setProposalID("");}}>Place legal hold</Button></li>)}</ul>}
        {selectedRequest && <form onSubmit={(event: SyntheticEvent<HTMLFormElement>) => {event.preventDefault(); if (!holdValid) return; void execute(async () => {const current=await getPrivacyDeletion(selectedRequest.id); if(current.status!=="SCHEDULED") throw new Error("The deletion request changed. Refresh before placing a hold."); await placePrivacyHold(current.id,{owner:holdOwner,reason_reference:holdReason.trim()},session.csrf_token);},"Legal hold recorded for the selected request.");}}><fieldset disabled={pending}><legend>Hold request {selectedRequest.id}</legend><label>Data owner<input required pattern="[a-z_]+" maxLength={128} value={holdOwner} onChange={event => {setHoldOwner(event.target.value);}} placeholder="identity" autoComplete="off" /></label><label>Legal reference<input required minLength={8} maxLength={256} value={holdReason} onChange={event => {setHoldReason(event.target.value);}} placeholder="Case or order reference" /></label><Button disabled={!holdValid || pending}>Record legal hold</Button><Button type="button" variant="secondary" onClick={() => {setRequestID("");}}>Cancel</Button></fieldset></form>}
      </section>
      <section className="surface-card"><h2>Active legal holds</h2><p>Releasing a hold requires a different current country administrator and the hold’s latest revision.</p>{holds.length === 0 ? <p>No active holds.</p> : <ul className="privacy-list">{holds.map(item => <li key={item.id}><strong>Hold {item.id} · {item.owner}</strong><span>Request {item.request_id} · revision {item.revision}</span><small>Recorded {when(item.recorded_at)} · by {item.recorded_by ?? "historical authority unknown"} · reference {item.reason_reference}</small><Button variant="secondary" disabled={pending || !item.recorded_by || item.recorded_by === session.subject_id} onClick={() => {setHoldID(item.id); setProposalID("");}}>{item.recorded_by === session.subject_id ? "Another administrator must release" : "Review release"}</Button></li>)}</ul>}
        {selectedHold?.recorded_by && selectedHold.recorded_by !== session.subject_id && <form onSubmit={(event: SyntheticEvent<HTMLFormElement>) => {event.preventDefault(); if (!reasonValid(releaseReason)) return; void execute(() => releasePrivacyHold(selectedHold.id,selectedHold.revision,releaseReason.trim(),session.csrf_token),"Legal hold released after independent review.");}}><fieldset disabled={pending}><legend>Release hold {selectedHold.id}</legend><p>Request {selectedHold.request_id} · owner {selectedHold.owner} · current revision {selectedHold.revision}</p><label>Release reference<input required minLength={8} maxLength={256} value={releaseReason} onChange={event => {setReleaseReason(event.target.value);}} placeholder="Approved release reference" /></label><Button disabled={!reasonValid(releaseReason) || pending}>Confirm independent release</Button><Button type="button" variant="secondary" onClick={() => {setHoldID(null);}}>Cancel</Button></fieldset></form>}
      </section>
    </>}
  </div>;
}
