import type { Session } from '@/api/types'

// Natural (human) ordering: digit runs compare as numbers, so "host2" sorts
// before "host10", and case and accents are ignored ("a" = "A" = "ä"). One
// collator for every list, so the dashboard and the sidebar cannot drift apart.
const collator = new Intl.Collator(undefined, { numeric: true, sensitivity: 'base' })

/**
 * compareNames orders two names naturally. Names the collator calls equal
 * ("Prod" and "prod") fall back to a plain code-unit comparison, so the order
 * is total and never depends on the order the names arrived in.
 */
export function compareNames(a: string, b: string): number {
  const c = collator.compare(a, b)
  if (c !== 0) return c
  return a < b ? -1 : a > b ? 1 : 0
}

/**
 * compareSessions orders by name, then by id for sessions that share one —
 * names are not unique, and a tie must not fall back to creation order.
 */
export function compareSessions(a: Session, b: Session): number {
  return compareNames(a.name, b.name) || compareNames(a.id, b.id)
}
