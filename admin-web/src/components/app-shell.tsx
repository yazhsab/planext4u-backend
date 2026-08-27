import { ChevronDown, Menu, ShieldCheck, X } from "lucide-react";
import { useState } from "react";
import { NavLink, Outlet } from "react-router";

import type { AdminSession } from "../api/client";
import { cn } from "../lib/utils";
import { Button } from "./button";

type AppShellProps = {
  session: AdminSession;
  countryChanging: boolean;
  onCountryChange: (country: string) => void;
};

const countryNames = new Intl.DisplayNames([navigator.language], {type: "region"});

export function AppShell({session, countryChanging, onCountryChange}: AppShellProps) {
  const [menuOpen, setMenuOpen] = useState(false);
  const closeMenu = () => { setMenuOpen(false); };

  return (
    <div className="app-frame">
      <a className="skip-link" href="#main-content">Skip to main content</a>
      <header className="topbar">
        <div className="brand-lockup" aria-label="Planext4u administration">
          <span className="brand-mark" aria-hidden="true">P4</span>
          <span><strong>Planext4u</strong><small>Administration</small></span>
        </div>
        <div className="topbar-actions">
          <label className="country-control">
            <span>Country context</span>
            <span className="select-wrap">
              <select
                aria-label="Country context"
                value={session.selected_country}
                disabled={countryChanging}
								onChange={(event) => { onCountryChange(event.target.value); }}
              >
                {session.allowed_countries.map((country) => <option key={country} value={country}>{countryNames.of(country) ?? country} ({country})</option>)}
              </select>
              <ChevronDown aria-hidden="true" size={16} />
            </span>
          </label>
          <div className="identity-summary" title={session.roles.join(", ")}>
            <span className="avatar" aria-hidden="true">{initials(session.display_name)}</span>
            <span><strong>{session.display_name}</strong><small>{readableRole(session.roles[0] ?? "AUDITOR")}</small></span>
          </div>
          <Button
            className="mobile-menu-button"
            variant="quiet"
            aria-expanded={menuOpen}
            aria-controls="primary-navigation"
            aria-label={menuOpen ? "Close navigation" : "Open navigation"}
						onClick={() => { setMenuOpen((value) => !value); }}
          >
            {menuOpen ? <X aria-hidden="true" /> : <Menu aria-hidden="true" />}
          </Button>
        </div>
      </header>

			<aside id="primary-navigation" className={cn("sidebar", menuOpen && "sidebar-open")}>
				<nav aria-label="Primary navigation">
          <p className="nav-label">Administration</p>
          {session.navigation.map((item) => (
            <NavLink
              key={item.id}
              to={item.path}
              end={item.path === "/"}
              onClick={closeMenu}
              className={({isActive}) => cn("nav-link", isActive && "nav-link-active")}
            >
              <span>{item.label}</span>
            </NavLink>
          ))}
        </nav>
        <div className="security-summary">
          <ShieldCheck aria-hidden="true" size={18} />
          <span><strong>Protected session</strong><small>MFA verified · server-authorized</small></span>
        </div>
      </aside>

      {menuOpen ? <button className="nav-scrim" aria-label="Close navigation" onClick={closeMenu} /> : null}
      <main id="main-content" className="main-content" tabIndex={-1}>
        <Outlet context={session} />
      </main>
    </div>
  );
}

function initials(name: string): string {
  return name.split(/\s+/).slice(0, 2).map((part) => part[0] ?? "").join("").toUpperCase();
}

function readableRole(role: string): string {
  return role.toLowerCase().split("_").map((word) => (word[0]?.toUpperCase() ?? "") + word.slice(1)).join(" ");
}
