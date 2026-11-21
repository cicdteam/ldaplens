// Checks the links of every tracked Markdown file: a relative link, or one to
// this repository's main branch on GitHub, must name a file that exists and,
// with #anchor, a heading in it (GitHub's anchor rules). External links are
// not fetched.
// Usage: node scripts/md-links.mjs (from the repository root)
import { execFileSync } from 'node:child_process'
import { existsSync, readFileSync, statSync } from 'node:fs'
import { dirname, join, normalize } from 'node:path'

const repoURL = 'https://github.com/cicdteam/ldaplens/blob/main/'
const files = execFileSync('git', ['ls-files', '*.md'], { encoding: 'utf8' })
  .split('\n')
  .filter(Boolean)

// The lines of a file, with fenced code blocks blanked.
function outsideFences(file) {
  let fence = false
  return readFileSync(file, 'utf8')
    .split('\n')
    .map((l) => {
      if (/^\s*(```|~~~)/.test(l)) {
        fence = !fence
        return ''
      }
      return fence ? '' : l
    })
}

// The heading anchors of a file as GitHub makes them: lower case, punctuation
// but - and _ dropped, spaces to -, and -1, -2 on repeats.
const cache = new Map()
function anchors(file) {
  if (!cache.has(file)) {
    const seen = new Map()
    const set = new Set()
    for (const l of outsideFences(file)) {
      const m = /^#{1,6} (.*)/.exec(l)
      if (!m) continue
      const base = m[1]
        .trim()
        .toLowerCase()
        .replace(/[^\p{L}\p{N}_\- ]/gu, '')
        .replace(/ /g, '-')
      const n = seen.get(base) ?? 0
      seen.set(base, n + 1)
      set.add(n ? `${base}-${n}` : base)
    }
    cache.set(file, set)
  }
  return cache.get(file)
}

let failed = 0
for (const file of files) {
  // Inline code is not a link.
  const text = outsideFences(file)
    .map((l) => l.replace(/`[^`]*`/g, ''))
    .join('\n')
  for (const m of text.matchAll(/\]\(([^)\s]+)\)/g)) {
    let target = m[1]
    if (target.startsWith(repoURL)) target = '/' + target.slice(repoURL.length)
    else if (/^[a-z][a-z+.-]*:/i.test(target)) continue
    const [path, anchor] = target.split('#')
    const to =
      path === '' ? file : normalize(path.startsWith('/') ? path.slice(1) : join(dirname(file), path))
    if (!existsSync(to)) {
      console.log(`${file}: ${m[1]}: no ${to}`)
      failed++
    } else if (anchor !== undefined && !(statSync(to).isFile() && anchors(to).has(anchor))) {
      console.log(`${file}: ${m[1]}: no heading #${anchor} in ${to}`)
      failed++
    }
  }
}
if (failed) {
  console.log(`md-links: ${failed} broken link(s)`)
  process.exit(1)
}
console.log(`md-links: ok (${files.length} files)`)
