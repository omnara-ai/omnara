export function canManageOrg(role: string): boolean {
  return role === 'owner' || role === 'admin'
}
