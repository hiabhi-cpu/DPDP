// homePathForRole is the landing route for a role. reception is a least-
// privilege operator that only ever sees the consent queue.
export function homePathForRole(role: string): string {
  return role === "reception" ? "/reception" : "/";
}
