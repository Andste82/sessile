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
