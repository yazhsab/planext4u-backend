import { CheckCircle2, Clock3, Globe2, ShieldCheck } from "lucide-react";
import { useOutletContext } from "react-router";

import type { AdminSession } from "../api/client";
import { Badge } from "../components/badge";

export function WorkspacePage() {
  const session = useOutletContext<AdminSession>();
  const authTime = new Intl.DateTimeFormat(undefined, {dateStyle: "medium", timeStyle: "short"}).format(new Date(session.assurance.auth_time));

  return (
    <div className="page-stack">
      <header className="page-header">
        <div><p className="eyebrow">Secure workspace</p><h1>Administration overview</h1></div>
        <Badge tone={session.assurance.fresh_auth ? "success" : "warning"}>{session.assurance.fresh_auth ? "Fresh authentication" : "Re-authentication due"}</Badge>
      </header>
      <p className="page-intro">Your workspace is scoped by role, tenant, and country. Navigation and data access are authorized by the server on every request.</p>

      <section className="detail-grid" aria-label="Current access context">
        <article className="detail-card">
          <span className="detail-icon"><Globe2 aria-hidden="true" /></span>
          <div><h2>Country context</h2><p>{countryNames.of(session.selected_country) ?? session.selected_country}</p><small>Switch from the header when your role allows more than one country.</small></div>
        </article>
        <article className="detail-card">
          <span className="detail-icon"><ShieldCheck aria-hidden="true" /></span>
          <div><h2>Assigned roles</h2><p>{session.roles.map(readableRole).join(", ")}</p><small>{session.capabilities.length} server-issued capabilities are active.</small></div>
        </article>
        <article className="detail-card">
          <span className="detail-icon"><CheckCircle2 aria-hidden="true" /></span>
          <div><h2>Multi-factor security</h2><p>{session.assurance.mfa_satisfied ? "Verified" : "Required"}</p><small>Sensitive administrator access requires verified MFA.</small></div>
        </article>
        <article className="detail-card">
          <span className="detail-icon"><Clock3 aria-hidden="true" /></span>
          <div><h2>Authentication time</h2><p>{authTime}</p><small>Sensitive changes will require a recent authentication.</small></div>
        </article>
      </section>

      <section className="access-section">
        <div><p className="eyebrow">Granted access</p><h2>Capabilities in this session</h2></div>
        <ul className="capability-list">
          {session.capabilities.map((capability) => <li key={capability}><CheckCircle2 aria-hidden="true" size={16} />{capability}</li>)}
        </ul>
      </section>
    </div>
  );
}

const countryNames = new Intl.DisplayNames([navigator.language], {type: "region"});

function readableRole(role: string): string {
  return role.toLowerCase().split("_").map((word) => (word[0]?.toUpperCase() ?? "") + word.slice(1)).join(" ");
}
