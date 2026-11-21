import { readFileSync, readdirSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { contrast } from './color'

const css = readFileSync(new URL('../index.css', import.meta.url), 'utf8')

// The custom properties declared in the first `<selector> {` block of index.css.
function vars(selector: string): Record<string, string> {
  const start = css.indexOf(`${selector} {`)
  if (start < 0) throw new Error(`no ${selector} block in index.css`)
  const body = css.slice(start, css.indexOf('}', start))
  return Object.fromEntries(
    [...body.matchAll(/(--[\w-]+):\s*([^;]+);/g)].map((m) => [m[1], m[2].trim()]),
  )
}

const themes = { light: vars(':root'), dark: vars(":root[data-theme='dark']") }

const pairs: [string, string, number][] = [
  ['--fg', '--bg', 7],
  ['--fg', '--panel', 7],
  ...['--muted', '--accent-text', '--danger', '--warn', '--ok'].flatMap((fg) =>
    ['--bg', '--panel', '--chrome'].map((bg): [string, string, number] => [fg, bg, 4.5]),
  ),
  // Muted text sits on hovered and selected rows too (results parent DN, tree prefix, palette).
  ['--muted', '--hover', 4.5],
  ['--muted', '--selected', 4.5],
  ['--accent-fg', '--accent', 4.5],
  ['--danger-fg', '--danger', 4.5],
]

// A badge's tone fill (bg-<tone>/10) over a surface, as the browser composites it: in sRGB.
function fill(tone: string, surface: string): string {
  const ch = (h: string, i: number) => parseInt(h.slice(i, i + 2), 16)
  return `#${[1, 3, 5]
    .map((i) => Math.round(0.1 * ch(tone, i) + 0.9 * ch(surface, i)))
    .map((c) => c.toString(16).padStart(2, '0'))
    .join('')}`
}

const fills = ['--danger', '--warn', '--ok'].flatMap((tone) =>
  ['--bg', '--chrome', '--panel', '--hover'].map((s): [string, string] => [tone, s]),
)

describe.each(Object.entries(themes))('%s theme', (_, t) => {
  it.each(pairs)('%s on %s is at least %d:1', (fg, bg, min) => {
    expect(t[fg], fg).toMatch(/^#[0-9a-f]{6}$/i)
    expect(t[bg], bg).toMatch(/^#[0-9a-f]{6}$/i)
    expect(contrast(t[fg], t[bg])).toBeGreaterThanOrEqual(min)
  })
  it.each(fills)('%s text on its badge fill over %s is at least 4.5:1', (tone, s) => {
    expect(t[s], s).toMatch(/^#[0-9a-f]{6}$/i)
    expect(contrast(t[tone], fill(t[tone], t[s]))).toBeGreaterThanOrEqual(4.5)
  })
})

it('repeats the dark values for the system dark scheme', () => {
  expect(vars(":root:not([data-theme='light'])")).toEqual(themes.dark)
})

// Accent as text fails 4.5:1 on dark; the class (text-accent, text-(--accent),
// text-[var(--accent)]) and a style object's color are caught, var() fallbacks included.
it('colors accent text with accent-text, never the accent', () => {
  const src = new URL('..', import.meta.url)
  const offenders = readdirSync(src, { recursive: true, encoding: 'utf8' })
    .filter((f) => /\.tsx?$/.test(f) && !/\.test\.tsx?$/.test(f))
    .filter((f) => {
      const code = readFileSync(new URL(f, src), 'utf8')
      return (
        /\btext-(?:accent|\(--accent|\[var\(--accent)(?![-\w])/.test(code) ||
        /(?<![\w-])color['"]?\s*:\s*['"`]?var\(--accent(?![-\w])/.test(code)
      )
    })
  expect(offenders).toEqual([])
})
