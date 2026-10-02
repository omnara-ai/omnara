/** Stable reorder that lists Omnara-managed (cluster) resources before org-managed ones. */
export function clusterFirst<TItem extends { management_kind: string }>(items: TItem[]) {
  return [
    ...items.filter((item) => item.management_kind === 'cluster'),
    ...items.filter((item) => item.management_kind !== 'cluster'),
  ]
}
