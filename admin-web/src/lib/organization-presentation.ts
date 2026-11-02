import type {AdminSession} from "../api/client";
import type {OrganizationWorkspace} from "../api/organization";

// Presentation only. Every command still resolves current authority in the
// owning service; editing this view or its fields grants no permission.
export function presentOrganizationAction(session: AdminSession,workspace: OrganizationWorkspace,nodeID: string,capability: string): boolean {
  if (session.roles.includes("SUPER_ADMIN")) return true;
  const path = new Set<string>();
  let current = workspace.nodes.find((node) => node.id === nodeID);
  while (current) {
    if (!current.active || path.has(current.id)) return false;
    path.add(current.id);
    current = workspace.nodes.find((node) => node.id === current?.parent_id);
  }
  return workspace.staff.some((assignment) => assignment.subject_id === session.subject_id && assignment.status === "ACTIVE" && path.has(assignment.node_id) && workspace.roles.some((role) => role.id === assignment.role_id && role.capabilities.includes(capability)));
}
