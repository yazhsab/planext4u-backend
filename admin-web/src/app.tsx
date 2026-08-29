import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, LockKeyhole, ShieldX } from "lucide-react";
import { Navigate, Route, Routes } from "react-router";

import { APIError, getSession, setCountry } from "./api/client";
import { AppShell } from "./components/app-shell";
import { StatePanel } from "./components/state-panel";
import { AuditPage } from "./pages/audit-page";
import { OperationsPage } from "./pages/operations-page";
import { WorkspacePage } from "./pages/workspace-page";
import { GovernancePage } from "./pages/governance-page";

export function App() {
  const queryClient = useQueryClient();
  const sessionQuery = useQuery({
    queryKey: ["admin", "session"],
    queryFn: ({signal}) => getSession(signal),
    staleTime: 30_000,
    retry: (attempt, error) => !(error instanceof APIError && error.status < 500) && attempt < 2,
  });
  const countryMutation = useMutation({
    mutationFn: async (country: string) => {
      if (!sessionQuery.data) throw new Error("Session unavailable");
      await setCountry(country, sessionQuery.data.csrf_token);
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({queryKey: ["admin"]});
    },
  });

  if (sessionQuery.isPending) return <SessionLoading />;
  if (sessionQuery.isError) return <SessionError error={sessionQuery.error} retry={() => void sessionQuery.refetch()} />;

  return (
    <Routes>
			<Route element={<AppShell session={sessionQuery.data} countryChanging={countryMutation.isPending} onCountryChange={(country) => { countryMutation.mutate(country); }} />}>
        <Route index element={<WorkspacePage />} />
        <Route path="operations" element={<OperationsPage />} />
        <Route path="governance" element={<GovernancePage />} />
        <Route path="audit" element={<AuditPage />} />
        <Route path="*" element={<Navigate to="/" replace />} />
      </Route>
    </Routes>
  );
}

function SessionLoading() {
  return (
    <main className="session-gate" aria-busy="true">
      <div className="gate-brand"><span className="brand-mark">P4</span><strong>Planext4u Administration</strong></div>
      <div className="gate-skeleton" role="status"><span /><span /><span /><p>Verifying your secure administrator session…</p></div>
    </main>
  );
}

function SessionError({error, retry}: {error: Error; retry: () => void}) {
  if (error instanceof APIError && error.status === 401) {
    return <GateState icon={LockKeyhole} title="Administrator sign-in required" message={error.message} linkLabel="Go to secure sign-in" link="/login" />;
  }
  if (error instanceof APIError && error.code === "ADMIN_MFA_REQUIRED") {
    return <GateState icon={ShieldX} title="Multi-factor verification required" message="Complete multi-factor authentication before opening the administration workspace." linkLabel="Verify identity" link="/login?reauth=mfa" />;
  }
  return (
    <main className="session-gate">
      <div className="gate-brand"><span className="brand-mark">P4</span><strong>Planext4u Administration</strong></div>
      <StatePanel icon={AlertTriangle} tone="danger" title="Administration service unavailable" message={error instanceof APIError ? `${error.message} Reference: ${error.correlationID}` : "Check your connection and try again."} actionLabel="Try again" onAction={retry} />
    </main>
  );
}

function GateState({icon: Icon, title, message, linkLabel, link}: {icon: typeof LockKeyhole; title: string; message: string; linkLabel: string; link: string}) {
  return (
    <main className="session-gate">
      <div className="gate-brand"><span className="brand-mark">P4</span><strong>Planext4u Administration</strong></div>
      <section className="gate-card">
        <span className="gate-icon"><Icon aria-hidden="true" /></span>
        <p className="eyebrow">Protected workspace</p>
        <h1>{title}</h1>
        <p>{message}</p>
        <a className="button-link" href={link}>{linkLabel}</a>
        <small>Sessions use a secure, HTTP-only cookie and are never stored in browser storage.</small>
      </section>
    </main>
  );
}
