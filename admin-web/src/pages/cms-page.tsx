import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, ArrowDown, ArrowUp, FilePenLine, Flag, Plus, Save, Send, Settings2, Trash2 } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { useOutletContext } from "react-router";

import {
  APIError,
  getCMSWorkspace,
  listCMSPageDrafts,
  saveCMSPageDraft,
  saveCMSWorkspace,
  submitOperation,
  type AdminSession,
  type CMSPage,
  type CMSPageBlock,
  type CMSPageDraft,
  type CMSWorkspace,
} from "../api/client";
import { Button } from "../components/button";
import { StatePanel } from "../components/state-panel";
import {useAudienceDirectory} from "../lib/audience-directory";
import {t} from "../lib/i18n";

type EditorTab = "pages" | "workspace";
type ContentDrafts = Record<string, string>;

const blockKinds = ["HERO", "MODULE_NAV", "TRUST_STRIP", "CATEGORY_RAIL", "ITEM_RAIL", "SERVICE_RAIL", "BANNER", "ONBOARDING"];
const audiences = ["PUBLIC", "CUSTOMER", "VENDOR", "RIDER"] as const;

export function CMSPage() {
  const session = useOutletContext<AdminSession>();
  const [tab, setTab] = useState<EditorTab>("pages");

  if (!session.capabilities.includes("admin.config.manage")) {
    return <StatePanel icon={AlertTriangle} title="Page builder access unavailable" message="Your server-authorized role does not include configuration management." />;
  }

  return (
    <div className="page-stack">
      <header className="page-header">
        <div><p className="eyebrow">Country content control</p><h1>CMS page builder</h1></div>
        <span className="badge badge-success">{session.selected_country} draft workspace</span>
      </header>
      <p className="page-intro">Build audience-specific pages and global app configuration. Drafts use optimistic revisions; publication is submitted through the independently approved CMS operation flow.</p>

      <OnboardingDraftCreator session={session} />
      <OnboardingDraftCreator session={session} startup />

      <div className="cms-tabs" role="tablist" aria-label="CMS editor">
        <button type="button" role="tab" aria-selected={tab === "pages"} onClick={() => { setTab("pages"); }}><FilePenLine aria-hidden="true" size={17} /> Page drafts</button>
        <button type="button" role="tab" aria-selected={tab === "workspace"} onClick={() => { setTab("workspace"); }}><Settings2 aria-hidden="true" size={17} /> App workspace</button>
      </div>

      {tab === "pages" ? <PageDraftEditor session={session} /> : <WorkspaceEditor session={session} />}
    </div>
  );
}

function OnboardingDraftCreator({session,startup=false}: {session: AdminSession;startup?:boolean}) {
  const kind=startup ? "splash" : "onboarding";
  const label=startup ? "Startup splash" : "Introduction";
  const inputPrefix=startup ? "startup" : "onboarding";
  const queryClient = useQueryClient();
  const [open, setOpen] = useState(false);
  const [role, setRole] = useState("customer");
  const [locale, setLocale] = useState("en");
  const [version, setVersion] = useState("1");
  const [title, setTitle] = useState("");
  const [message, setMessage] = useState("");
  const mutation = useMutation({
    mutationFn: () => {
      if (!/^[A-Za-z0-9._-]{1,32}$/.test(version) || !title.trim() || title.length > 120 || !message.trim() || message.length > 1000) {
        throw new Error("Enter an edition, title and introduction within the displayed limits.");
      }
      const page: CMSPage = {
        id: `mobile-${kind}-${role}-${locale}`, route: `/app/${kind}/${role}/${locale}`,
        title_key: `${kind}.${role}.${locale}`, audience: startup ? ["PUBLIC"] : [role === "customer" ? "CUSTOMER" : role === "vendor" ? "VENDOR" : "RIDER"], enabled: true,
        blocks: [{id: "introduction", kind: "ONBOARDING", enabled: true, priority: 0,
          content: {version, title: title.trim(), message: message.trim()}}],
      };
      return saveCMSPageDraft(page.id, 0, page, session.csrf_token);
    },
    onSuccess: () => { void queryClient.invalidateQueries({queryKey: ["admin", "cms", "pages"]}); },
  });
  return <section className="cms-editor" aria-label={startup ? "Mobile startup splash drafts" : "Mobile onboarding drafts"}>
    <div className="cms-panel-heading cms-introduction-heading"><div><h2>{startup ? "Mobile startup splash" : "Mobile introductions"}</h2><p>{startup ? "Public welcome content before sign-in" : "Versioned education for each application and language"}</p></div><Button variant="secondary" onClick={() => { setOpen(!open); }}>{startup ? "Create startup splash" : "Create mobile introduction"}</Button></div>
    {open && <form className="cms-form-grid" onSubmit={(event) => { event.preventDefault(); mutation.mutate(); }}>
      <p className="cms-field-wide page-intro">{startup ? "Startup splash appears before sign-in. Choose a role and language, save the public draft, then request independent publication approval. The app can continue when startup content is unavailable." : "Choose a role and language. Save the draft, then use the page editor and independent publication approval. Mobile introductions appear after sign-in; the native startup introduction remains available offline."}</p>
      <div className="cms-form-field"><label htmlFor={inputPrefix+"-role"}>{startup ? "Startup application role" : "Application role"}</label><select id={inputPrefix+"-role"} value={role} disabled={mutation.isPending} onChange={(event) => { setRole(event.target.value); }}>
        {["customer","vendor","rider"].map((value) => <option key={value} value={value}>{value}</option>)}
      </select></div>
      <div className="cms-form-field"><label htmlFor={inputPrefix+"-language"}>{label} language</label><select id={inputPrefix+"-language"} value={locale} disabled={mutation.isPending} onChange={(event) => { setLocale(event.target.value); }}>
        {["en","ta","hi","te","kn","ml","mr","bn","gu"].map((value) => <option key={value} value={value}>{value}</option>)}
      </select></div>
      <div className="cms-form-field"><label htmlFor={inputPrefix+"-edition"}>{label} edition</label><input id={inputPrefix+"-edition"} value={version} maxLength={32} required disabled={mutation.isPending} onChange={(event) => { setVersion(event.target.value); }} /></div>
      <div className="cms-form-field"><label htmlFor={inputPrefix+"-title"}>{label} title</label><input id={inputPrefix+"-title"} value={title} maxLength={120} required disabled={mutation.isPending} onChange={(event) => { setTitle(event.target.value); }} /></div>
      <div className="cms-form-field cms-field-wide"><label htmlFor={inputPrefix+"-message"}>{label} message</label><textarea id={inputPrefix+"-message"} value={message} maxLength={1000} required disabled={mutation.isPending} onChange={(event) => { setMessage(event.target.value); }} /></div>
      <div className="cms-field-wide"><Button type="submit" disabled={mutation.isPending}>{startup ? "Save startup splash draft" : "Save introduction draft"}</Button></div>
      {mutation.isError && <p className="cms-field-wide" role="alert">{errorMessage(mutation.error)}</p>}
      {mutation.isSuccess && <p className="cms-field-wide" role="status">{label} draft saved. Review it in the page editor before submitting publication.</p>}
    </form>}
  </section>;
}

function PageDraftEditor({session}: {session: AdminSession}) {
  const {query:groupsQuery,groups:audienceGroups}=useAudienceDirectory(session);
  const queryClient = useQueryClient();
  const draftsQuery = useQuery({
    queryKey: ["admin", "cms", "pages", session.selected_country],
    queryFn: ({signal}) => listCMSPageDrafts(signal),
  });
  const [selectedID, setSelectedID] = useState("");
  const [page, setPage] = useState<CMSPage | null>(null);
  const [revision, setRevision] = useState(0);
  const [contentDrafts, setContentDrafts] = useState<ContentDrafts>({});
  const [dirty, setDirty] = useState(false);
  const [validationError, setValidationError] = useState("");
  const [publishReason, setPublishReason] = useState("");
  const [notice, setNotice] = useState("");

  const drafts = draftsQuery.data?.items ?? [];
  const publishedRevision=draftsQuery.data?.published_revision;
  const selectedDraft = useMemo(() => drafts.find((draft) => draft.page.id === selectedID) ?? drafts[0], [drafts, selectedID]);

  useEffect(() => {
    if (!selectedDraft || dirty) return;
    setSelectedID(selectedDraft.page.id);
    setPage(structuredClone(selectedDraft.page));
    setRevision(selectedDraft.revision);
    setContentDrafts(toContentDrafts(selectedDraft.page));
    setValidationError("");
  }, [dirty, selectedDraft]);

  const saveMutation = useMutation({
    mutationFn: async () => {
      if (!page) throw new Error("Select a page draft first.");
      const validated = validatePage(page, contentDrafts);
      if (!validated.ok) throw new Error(validated.message);
      return saveCMSPageDraft(page.id, revision, validated.page, session.csrf_token);
    },
    onSuccess: async (saved) => {
      setPage(structuredClone(saved.page));
      setRevision(saved.revision);
      setContentDrafts(toContentDrafts(saved.page));
      setDirty(false);
      setValidationError("");
      setNotice(`Draft revision ${String(saved.revision)} saved.`);
      await queryClient.invalidateQueries({queryKey: ["admin", "cms", "pages"]});
    },
    onError: (error) => { setValidationError(errorMessage(error)); },
  });
  const publishMutation = useMutation({
    mutationFn: async () => {
      if (!page) throw new Error("Select a page draft first.");
      if (dirty) throw new Error("Save the draft before requesting publication.");
      if(publishedRevision===undefined || publishedRevision<1)throw new Error("Refresh publication details before requesting publication.");
      if (publishReason.trim().length < 8) throw new Error("Provide a verified publication reason of at least 8 characters.");
      return submitOperation({
        domain: "CMS",
        action: "PUBLISH",
        target_id: page.id,
        reason: publishReason.trim(),
        payload: {publication_type:"PAGE",expected_draft_revision:revision,expected_snapshot_revision:publishedRevision},
      }, session.csrf_token);
    },
    onSuccess: (change) => {
      setNotice(change.status === "EXECUTED" ? "Page publication executed." : "Publication submitted for independent approval.");
      setPublishReason("");
    },
    onError: (error) => { setValidationError(errorMessage(error)); },
  });

  if (draftsQuery.isPending) return <EditorLoading label="Loading country page drafts…" />;
  if (draftsQuery.isError) return <StatePanel icon={AlertTriangle} tone="danger" title="Page drafts could not be loaded" message={errorMessage(draftsQuery.error)} actionLabel="Try again" onAction={() => { void draftsQuery.refetch(); }} />;
  if (drafts.length === 0) return <StatePanel icon={FilePenLine} title="No page drafts" message="Create a mobile introduction or startup splash to begin." />;
  if (!page) return <EditorLoading label="Preparing page editor…" />;

  const updatePage = (next: CMSPage) => { setPage(next); setDirty(true); setNotice(""); };
  const switchDraft = (draft: CMSPageDraft) => {
    if (dirty && !window.confirm("Discard unsaved edits and open another page?")) return;
    setSelectedID(draft.page.id);
    setPage(structuredClone(draft.page));
    setRevision(draft.revision);
    setContentDrafts(toContentDrafts(draft.page));
    setDirty(false);
    setValidationError("");
    setNotice("");
  };
  const updateBlock = (index: number, patch: Partial<CMSPageBlock>) => {
    const blocks = page.blocks.map((block, blockIndex) => blockIndex === index ? {...block, ...patch} : block);
    updatePage({...page, blocks: normalizePriorities(blocks)});
  };
  const moveBlock = (index: number, offset: -1 | 1) => {
    const target = index + offset;
    if (target < 0 || target >= page.blocks.length) return;
    const blocks = [...page.blocks];
    const current = blocks[index];
    const destination = blocks[target];
    if (!current || !destination) return;
    blocks[index] = destination;
    blocks[target] = current;
    updatePage({...page, blocks: normalizePriorities(blocks)});
  };
  const addBlock = () => {
    const id = `block-${globalThis.crypto.randomUUID()}`;
    const block: CMSPageBlock = {id, kind: "ITEM_RAIL", title_key: "New section", enabled: true, priority: page.blocks.length, content: {limit: 8}};
    updatePage({...page, blocks: [...page.blocks, block]});
    setContentDrafts((current) => ({...current, [id]: JSON.stringify(block.content, null, 2)}));
  };
  const removeBlock = (index: number) => {
    const block = page.blocks[index];
    if (!block || !window.confirm(`Remove block “${block.title_key ?? block.kind}” from this draft?`)) return;
    updatePage({...page, blocks: normalizePriorities(page.blocks.filter((_, blockIndex) => blockIndex !== index))});
    setContentDrafts((current) => Object.fromEntries(Object.entries(current).filter(([id]) => id !== block.id)));
  };

  return (
    <div className="cms-page-layout">
      <aside className="cms-page-list" aria-label="Page drafts">
        <div className="cms-panel-heading"><div><h2>Pages</h2><p>{String(drafts.length)} country drafts</p></div></div>
        {drafts.map((draft) => (
          <button key={draft.page.id} type="button" className={draft.page.id === page.id ? "cms-page-choice cms-page-choice-active" : "cms-page-choice"} onClick={() => { switchDraft(draft); }}>
            <strong>{draft.page.title_key}</strong><span>{draft.page.route}</span><small>Revision {String(draft.revision)}</small>
          </button>
        ))}
      </aside>

      <section className="cms-editor" aria-label={`Edit ${page.title_key}`}>
        <div className="cms-panel-heading">
          <div><h2>{page.title_key}</h2><p>Revision {String(revision)} · {dirty ? "Unsaved changes" : "Draft saved"}</p></div>
          <span className={page.enabled ? "badge badge-success" : "badge"}>{page.enabled ? "Enabled" : "Disabled"}</span>
        </div>

        <div className="cms-form-grid">
          <label>Page identifier<input value={page.id} readOnly /></label>
          <label>Route<input readOnly={page.id.startsWith("mobile-splash-")} value={page.route} onChange={(event) => { updatePage({...page, route: event.target.value}); }} /></label>
          <label className="cms-field-wide">Title key<input value={page.title_key} onChange={(event) => { updatePage({...page, title_key: event.target.value}); }} /></label>
          <fieldset className="cms-field-wide"><legend>Audience</legend><div className="cms-check-row">{audiences.map((audience) => <label key={audience}><input type="checkbox" disabled={page.id.startsWith("mobile-splash-")} checked={page.audience.includes(audience)} onChange={(event) => { const next = event.target.checked ? [...page.audience, audience] : page.audience.filter((value) => value !== audience); updatePage({...page, audience: next}); }} /> {audience}</label>)}</div></fieldset>
          {!page.id.startsWith("mobile-splash-") && <fieldset className="cms-field-wide"><legend>{t("audienceTitle")}</legend><p>{t("audiencePageHelp")}</p>
            {groupsQuery.isPending && <p role="status">{t("loading")}</p>}
            {groupsQuery.isError && <p role="status">{t("audienceFailed")}</p>}
            <Button type="button" variant="secondary" disabled={groupsQuery.isFetching} onClick={()=>{void groupsQuery.refetch();}}>{t("audienceRefresh")}</Button>
            <div className="cms-check-row">{audienceGroups.map(group=><label key={group.id}><input type="checkbox" checked={page.audience_groups?.includes(group.id)??false} disabled={!(page.audience_groups?.includes(group.id)??false) && (page.audience_groups?.length??0)>=16} onChange={event=>{const next=event.target.checked ? [...(page.audience_groups??[]),group.id] : (page.audience_groups??[]).filter(value=>value!==group.id);updatePage({...page,audience_groups:next});}} /> {group.name}{!group.active ? ` (${t("operatorSuspended")})` : ""}</label>)}</div>
            {groupsQuery.isSuccess && groupsQuery.hasNextPage && <Button type="button" variant="secondary" disabled={groupsQuery.isFetching} onClick={()=>{void groupsQuery.fetchNextPage();}}>{t("audienceMore")}</Button>}
            {(page.audience_groups??[]).filter(id=>!audienceGroups.some(group=>group.id===id)).map(id=><p key={id}>{t("audienceRetainedSelection")} {id}</p>)}
          </fieldset>}
          <label className="cms-switch"><input type="checkbox" checked={page.enabled} onChange={(event) => { updatePage({...page, enabled: event.target.checked}); }} /> Page available after publication</label>
        </div>

        <div className="cms-section-heading"><div><h3>Page blocks</h3><p>Order, enable and configure each page section.</p></div><Button type="button" variant="secondary" disabled={page.id.startsWith("mobile-splash-")} onClick={addBlock}><Plus aria-hidden="true" size={16} /> Add block</Button></div>
        <div className="cms-block-list">
          {page.blocks.map((block, index) => (
            <article className="cms-block" key={block.id}>
              <div className="cms-block-toolbar">
                <span className="cms-order">{String(index + 1)}</span>
                <div><strong>{block.title_key ?? block.kind}</strong><small>{block.id}</small></div>
                <div className="cms-icon-actions">
                  <button type="button" aria-label={`Move ${block.title_key ?? block.kind} up`} disabled={index === 0} onClick={() => { moveBlock(index, -1); }}><ArrowUp aria-hidden="true" size={16} /></button>
                  <button type="button" aria-label={`Move ${block.title_key ?? block.kind} down`} disabled={index === page.blocks.length - 1} onClick={() => { moveBlock(index, 1); }}><ArrowDown aria-hidden="true" size={16} /></button>
                  <button type="button" aria-label={`Remove ${block.title_key ?? block.kind}`} onClick={() => { removeBlock(index); }}><Trash2 aria-hidden="true" size={16} /></button>
                </div>
              </div>
              <div className="cms-form-grid">
                <label>Block type<select value={block.kind} onChange={(event) => { updateBlock(index, {kind: event.target.value}); }}>{blockKinds.map((kind) => <option key={kind}>{kind}</option>)}</select></label>
                <label>Title key<input value={block.title_key ?? ""} onChange={(event) => { updateBlock(index, {title_key: event.target.value || undefined}); }} /></label>
                <label className="cms-switch"><input type="checkbox" checked={block.enabled} onChange={(event) => { updateBlock(index, {enabled: event.target.checked}); }} /> Enabled</label>
                <label className="cms-field-wide">Block content (JSON object)<textarea className="code-input" rows={7} value={contentDrafts[block.id] ?? "{}"} onChange={(event) => { setContentDrafts((current) => ({...current, [block.id]: event.target.value})); setDirty(true); setNotice(""); }} spellCheck={false} /></label>
              </div>
            </article>
          ))}
        </div>

        <EditorActions
          dirty={dirty}
          saving={saveMutation.isPending}
          publishing={publishMutation.isPending}
          freshAuth={session.assurance.fresh_auth}
          publicationAvailable={publishedRevision!==undefined && publishedRevision>0 && !draftsQuery.isFetching}
          reason={publishReason}
          onReason={setPublishReason}
          onSave={() => { setValidationError(""); saveMutation.mutate(); }}
          onPublish={() => { setValidationError(""); publishMutation.mutate(); }}
        />
        <Button variant="secondary" disabled={draftsQuery.isFetching} onClick={()=>{void draftsQuery.refetch();}}>Refresh publication details</Button>
        {publishedRevision===0 && <p>Publish app workspace settings before publishing pages.</p>}
        <EditorFeedback error={validationError} notice={notice} />
      </section>
    </div>
  );
}

function WorkspaceEditor({session}: {session: AdminSession}) {
  const queryClient = useQueryClient();
  const query = useQuery({queryKey: ["admin", "cms", "workspace", session.selected_country], queryFn: ({signal}) => getCMSWorkspace(signal)});
  const publishedRevision=query.data?.published_revision;
  const [workspace, setWorkspace] = useState<CMSWorkspace | null>(null);
  const [revision, setRevision] = useState(0);
  const [dirty, setDirty] = useState(false);
  const [newFlag, setNewFlag] = useState("");
  const [reason, setReason] = useState("");
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");

  useEffect(() => {
    if (!query.data || dirty) return;
    setWorkspace(structuredClone(query.data.workspace));
    setRevision(query.data.revision);
  }, [dirty, query.data]);

  const saveMutation = useMutation({
    mutationFn: () => {
      if (!workspace) throw new Error("The workspace draft is unavailable.");
      return saveCMSWorkspace(revision, workspace, session.csrf_token);
    },
    onSuccess: async (saved) => {
      setWorkspace(structuredClone(saved.workspace)); setRevision(saved.revision); setDirty(false); setError(""); setNotice(`Workspace revision ${String(saved.revision)} saved.`);
      await queryClient.invalidateQueries({queryKey: ["admin", "cms", "workspace"]});
    },
    onError: (caught) => { setError(errorMessage(caught)); },
  });
  const publishMutation = useMutation({
    mutationFn: () => {
      if (dirty) throw new Error("Save the workspace draft before requesting publication.");
      if(publishedRevision===undefined)throw new Error("Refresh publication details before requesting publication.");
      if (reason.trim().length < 8) throw new Error("Provide a verified publication reason of at least 8 characters.");
      return submitOperation({domain: "CMS", action: "PUBLISH", target_id: "workspace", reason: reason.trim(), payload: {publication_type:"WORKSPACE",expected_draft_revision:revision,expected_snapshot_revision:publishedRevision}}, session.csrf_token);
    },
    onSuccess: (change) => { setNotice(change.status === "EXECUTED" ? "Workspace publication executed." : "Workspace publication submitted for independent approval."); setReason(""); },
    onError: (caught) => { setError(errorMessage(caught)); },
  });

  if (query.isPending) return <EditorLoading label="Loading app workspace draft…" />;
  if (query.isError) return <StatePanel icon={AlertTriangle} tone="danger" title="App workspace could not be loaded" message={errorMessage(query.error)} actionLabel="Try again" onAction={() => { void query.refetch(); }} />;
  if (!workspace) return <EditorLoading label="Preparing workspace editor…" />;

  const update = (next: CMSWorkspace) => { setWorkspace(next); setDirty(true); setNotice(""); };
  const sections = [...workspace.home_sections].sort((left, right) => left.priority - right.priority);
  const moveSection = (index: number, offset: -1 | 1) => {
    const target = index + offset;
    if (target < 0 || target >= sections.length) return;
    const current = sections[index];
    const destination = sections[target];
    if (!current || !destination) return;
    sections[index] = destination;
    sections[target] = current;
    update({...workspace, home_sections: sections.map((section, position) => ({...section, priority: position}))});
  };

  return (
    <section className="cms-editor cms-workspace-editor">
      <div className="cms-panel-heading"><div><h2>Global app workspace</h2><p>Revision {String(revision)} · {dirty ? "Unsaved changes" : "Draft saved"}</p></div><span className="badge"><Settings2 aria-hidden="true" size={14} /> Client configuration</span></div>
      <div className="cms-section-heading"><div><h3>Release versions</h3><p>Minimum and latest versions used by Android, iOS and WEB bootstrap.</p></div></div>
      <div className="cms-form-grid">
        <label>Android minimum<input value={workspace.minimum_versions.ANDROID} onChange={(event) => { update({...workspace, minimum_versions: {...workspace.minimum_versions, ANDROID: event.target.value}}); }} /></label>
        <label>Android latest<input value={workspace.latest_versions.ANDROID} onChange={(event) => { update({...workspace, latest_versions: {...workspace.latest_versions, ANDROID: event.target.value}}); }} /></label>
        <label>iOS minimum<input value={workspace.minimum_versions.IOS} onChange={(event) => { update({...workspace, minimum_versions: {...workspace.minimum_versions, IOS: event.target.value}}); }} /></label>
        <label>iOS latest<input value={workspace.latest_versions.IOS} onChange={(event) => { update({...workspace, latest_versions: {...workspace.latest_versions, IOS: event.target.value}}); }} /></label>
        <label>Web minimum<input value={workspace.minimum_versions.WEB} onChange={(event) => { update({...workspace, minimum_versions: {...workspace.minimum_versions, WEB: event.target.value}}); }} /></label>
        <label>Web latest<input value={workspace.latest_versions.WEB} onChange={(event) => { update({...workspace, latest_versions: {...workspace.latest_versions, WEB: event.target.value}}); }} /></label>
      </div>

      <div className="cms-section-heading"><div><h3>Contact matching privacy</h3><p>Optional for customers. Both people must allow the same published policy and save each other before a match appears. Saving here only changes the draft; publication needs independent approval.</p></div></div>
      {workspace.consent_policies.find((policy) => policy.purpose === "CONTACT_MATCHING") ? <div className="cms-form-grid">
        <label>Contact matching policy version<input
          value={workspace.consent_policies.find((policy) => policy.purpose === "CONTACT_MATCHING")?.policy_version ?? ""}
          pattern="[A-Za-z0-9][A-Za-z0-9._:-]{0,63}" maxLength={64} required
          onChange={(event) => { update({...workspace, consent_policies: workspace.consent_policies.map((policy) => policy.purpose === "CONTACT_MATCHING" ? {...policy, policy_version: event.target.value, required: false} : policy)}); }}
        /></label>
        <Button type="button" variant="secondary" onClick={() => { update({...workspace, consent_policies: workspace.consent_policies.filter((policy) => policy.purpose !== "CONTACT_MATCHING")}); }}>Remove from next publication</Button>
      </div> : <Button type="button" variant="secondary" onClick={() => { update({...workspace, consent_policies: [...workspace.consent_policies, {purpose: "CONTACT_MATCHING", policy_version: "contacts-v1", required: false}]}); }}>Add optional contact matching policy</Button>}

      <div className="cms-section-heading"><div><h3>Feature flags</h3><p>Server-published module and capability switches.</p></div></div>
      <div className="cms-flag-grid">
        {Object.entries(workspace.flags).sort(([left], [right]) => left.localeCompare(right)).map(([key, enabled]) => <label key={key}><span><Flag aria-hidden="true" size={15} /> {key}</span><input type="checkbox" checked={enabled} onChange={(event) => { update({...workspace, flags: {...workspace.flags, [key]: event.target.checked}}); }} /></label>)}
      </div>
      <div className="cms-inline-add"><label>New flag key<input value={newFlag} onChange={(event) => { setNewFlag(event.target.value.replace(/[^a-z0-9_.-]/gi, "").toLowerCase()); }} /></label><Button type="button" variant="secondary" disabled={!newFlag || Object.hasOwn(workspace.flags, newFlag)} onClick={() => { update({...workspace, flags: {...workspace.flags, [newFlag]: false}}); setNewFlag(""); }}><Plus aria-hidden="true" size={16} /> Add flag</Button></div>

      <div className="cms-section-heading"><div><h3>Home sections</h3><p>Global ordering and visibility used by supported app bootstrap clients.</p></div><Button type="button" variant="secondary" onClick={() => { const id = `section-${globalThis.crypto.randomUUID()}`; update({...workspace, home_sections: [...sections, {id, kind: "ITEM_RAIL", title_key: "New section", enabled: true, priority: sections.length}]}); }}><Plus aria-hidden="true" size={16} /> Add section</Button></div>
      <div className="cms-block-list">
        {sections.map((section, index) => <article className="cms-block cms-section-row" key={section.id}>
          <span className="cms-order">{String(index + 1)}</span>
          <label>Identifier<input value={section.id} readOnly /></label>
          <label>Kind<input value={section.kind} onChange={(event) => { update({...workspace, home_sections: sections.map((value, sectionIndex) => sectionIndex === index ? {...value, kind: event.target.value} : value)}); }} /></label>
          <label>Title key<input value={section.title_key} onChange={(event) => { update({...workspace, home_sections: sections.map((value, sectionIndex) => sectionIndex === index ? {...value, title_key: event.target.value} : value)}); }} /></label>
          <label className="cms-switch"><input type="checkbox" checked={section.enabled} onChange={(event) => { update({...workspace, home_sections: sections.map((value, sectionIndex) => sectionIndex === index ? {...value, enabled: event.target.checked} : value)}); }} /> Enabled</label>
          <div className="cms-icon-actions"><button type="button" aria-label={`Move ${section.title_key} up`} disabled={index === 0} onClick={() => { moveSection(index, -1); }}><ArrowUp aria-hidden="true" size={16} /></button><button type="button" aria-label={`Move ${section.title_key} down`} disabled={index === sections.length - 1} onClick={() => { moveSection(index, 1); }}><ArrowDown aria-hidden="true" size={16} /></button><button type="button" aria-label={`Remove ${section.title_key}`} onClick={() => { update({...workspace, home_sections: sections.filter((_, sectionIndex) => sectionIndex !== index).map((value, position) => ({...value, priority: position}))}); }}><Trash2 aria-hidden="true" size={16} /></button></div>
        </article>)}
      </div>

      <EditorActions dirty={dirty} saving={saveMutation.isPending} publishing={publishMutation.isPending} freshAuth={session.assurance.fresh_auth} publicationAvailable={publishedRevision!==undefined && !query.isFetching} reason={reason} onReason={setReason} onSave={() => { setError(""); saveMutation.mutate(); }} onPublish={() => { setError(""); publishMutation.mutate(); }} />
      <Button variant="secondary" disabled={query.isFetching} onClick={()=>{void query.refetch();}}>Refresh publication details</Button>
      <EditorFeedback error={error} notice={notice} />
    </section>
  );
}

function EditorActions({dirty, saving, publishing, freshAuth, publicationAvailable, reason, onReason, onSave, onPublish}: {dirty: boolean; saving: boolean; publishing: boolean; freshAuth: boolean;publicationAvailable:boolean; reason: string; onReason: (value: string) => void; onSave: () => void; onPublish: () => void}) {
  return <div className="cms-actions"><Button type="button" disabled={!dirty || saving} onClick={onSave}><Save aria-hidden="true" size={16} /> {saving ? "Saving…" : "Save draft"}</Button><label>Publication reason<input value={reason} onChange={(event) => { onReason(event.target.value); }} placeholder="Describe verified content changes" /></label><Button type="button" variant="secondary" disabled={dirty || publishing || !freshAuth || !publicationAvailable} onClick={onPublish}><Send aria-hidden="true" size={16} /> {publishing ? "Submitting…" : "Request publication"}</Button>{!publicationAvailable && <p>Refresh publication details before publishing.</p>}{!freshAuth ? <a href="/login?reauth=mfa">Re-authenticate before publishing</a> : null}</div>;
}

function EditorFeedback({error, notice}: {error: string; notice: string}) {
  if (error) return <p className="cms-feedback cms-feedback-error" role="alert">{error}</p>;
  if (notice) return <p className="cms-feedback" role="status">{notice}</p>;
  return null;
}

function EditorLoading({label}: {label: string}) {
  return <div className="table-skeleton" role="status" aria-label={label}>{Array.from({length: 5}, (_, index) => <span key={index} />)}</div>;
}

function toContentDrafts(page: CMSPage): ContentDrafts {
  return Object.fromEntries(page.blocks.map((block) => [block.id, JSON.stringify(block.content, null, 2)]));
}

function normalizePriorities(blocks: CMSPageBlock[]): CMSPageBlock[] {
  return blocks.map((block, index) => ({...block, priority: index}));
}

function validatePage(page: CMSPage, contentDrafts: ContentDrafts): {ok: true; page: CMSPage} | {ok: false; message: string} {
  if (!page.route.startsWith("/")) return {ok: false, message: "The page route must start with /."};
  if (!page.title_key.trim()) return {ok: false, message: "The page title key is required."};
  if (page.audience.length === 0) return {ok: false, message: "Select at least one page audience."};
  if (new Set(page.blocks.map((block) => block.id)).size !== page.blocks.length) return {ok: false, message: "Every block identifier must be unique."};
  const blocks: CMSPageBlock[] = [];
  for (const [index, block] of page.blocks.entries()) {
    if (!block.kind.trim()) return {ok: false, message: `Block ${String(index + 1)} requires a type.`};
    try {
      const content: unknown = JSON.parse(contentDrafts[block.id] ?? "{}");
      if (!content || typeof content !== "object" || Array.isArray(content)) return {ok: false, message: `Block ${String(index + 1)} content must be a JSON object.`};
      blocks.push({...block, content: content as Record<string, unknown>, priority: index});
    } catch {
      return {ok: false, message: `Block ${String(index + 1)} contains invalid JSON.`};
    }
  }
  return {ok: true, page: {...page, title_key: page.title_key.trim(), route: page.route.trim(), blocks}};
}

function errorMessage(error: unknown): string {
  if (error instanceof APIError) {
    if (error.status === 409) return `This draft changed on the server. Reload it before applying your edits. Reference: ${error.correlationID}`;
    return `${error.message} Reference: ${error.correlationID}`;
  }
  return error instanceof Error ? error.message : "The CMS operation could not be completed.";
}
