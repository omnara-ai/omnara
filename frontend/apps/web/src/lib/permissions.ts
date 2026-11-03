export function canManageOrg(role: string): boolean {
  return role === 'owner' || role === 'admin'
}

/**
 * Sharing org machines and machine pools with a project (and editing or
 * revoking those grants) requires project access management plus org
 * management, since the shared resource belongs to the organization.
 */
export function canManageMachineGrants(
  orgRole: string,
  projectAccess: { can_manage_access: boolean } | undefined,
): boolean {
  return canManageOrg(orgRole) && (projectAccess?.can_manage_access ?? false)
}
