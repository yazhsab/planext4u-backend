import type { LucideIcon } from "lucide-react";

import { Button } from "./button";

type StatePanelProps = {
  icon: LucideIcon;
  title: string;
  message: string;
  actionLabel?: string;
  onAction?: () => void;
  tone?: "default" | "danger";
};

export function StatePanel({icon: Icon, title, message, actionLabel, onAction, tone = "default"}: StatePanelProps) {
  return (
    <section className="state-panel" role={tone === "danger" ? "alert" : "status"} aria-live="polite">
      <span className="state-icon" data-tone={tone} aria-hidden="true"><Icon size={22} strokeWidth={1.8} /></span>
      <div>
        <h2>{title}</h2>
        <p>{message}</p>
      </div>
      {actionLabel && onAction ? <Button variant="secondary" onClick={onAction}>{actionLabel}</Button> : null}
    </section>
  );
}
