// Expressing one absolute POSIX path relative to another.
//
// The file browser can say where a file is in two ways: as the target itself
// names it, and as you would refer to it from the shell sitting next to it.
// The second is what makes a copied path directly pasteable into that shell,
// and it is the only one that needs computing.

function segments(path: string): string[] {
  return path.split('/').filter((s) => s !== '' && s !== '.')
}

/**
 * relativeTo expresses target relative to base, both absolute POSIX paths.
 *
 * Walks up with ".." where it has to, because a file above or beside the
 * shell's directory is an ordinary thing to want to refer to — "../logs/a.txt"
 * is a usable answer where refusing to produce one is not.
 *
 * Returns "." for the directory itself, and falls back to the absolute target
 * when the two share no root at all, which cannot happen for two paths on one
 * machine but keeps the function total.
 */
export function relativeTo(base: string, target: string): string {
  if (!base.startsWith('/') || !target.startsWith('/')) return target

  const from = segments(base)
  const to = segments(target)

  let common = 0
  while (common < from.length && common < to.length && from[common] === to[common]) common++

  const up = from.length - common
  const down = to.slice(common)
  if (up === 0 && down.length === 0) return '.'

  return [...Array<string>(up).fill('..'), ...down].join('/')
}

// Helpers for absolute remote (SSH host) paths, as the directory chooser
// shows them. Remote paths are the user's own on their own host, so these only
// shape strings for display and navigation; the server resolves them.

/** One breadcrumb: the label to show and the absolute path it jumps to. */
export interface Crumb {
  name: string
  path: string
}

/** remoteCrumbs splits an absolute path into jump targets, excluding "/". */
export function remoteCrumbs(path: string): Crumb[] {
  const segs = path.split('/').filter((s) => s !== '')
  return segs.map((name, i) => ({ name, path: '/' + segs.slice(0, i + 1).join('/') }))
}

/** joinRemote appends a child name to an absolute directory path. */
export function joinRemote(base: string, name: string): string {
  return base.endsWith('/') ? base + name : `${base}/${name}`
}

/** parentRemote returns the parent directory, or null at "/". */
export function parentRemote(path: string): string | null {
  const segs = path.split('/').filter((s) => s !== '')
  if (segs.length === 0) return null
  return segs.length === 1 ? '/' : '/' + segs.slice(0, -1).join('/')
}
