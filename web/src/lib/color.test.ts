import { describe, expect, it } from 'vitest'
import { contrast, textOn } from './color'

describe('textOn', () => {
  it.each([
    ['#f59e0b', '#000000'],
    ['#fff', '#000000'],
    ['#000', '#ffffff'],
    ['#b91c1c', '#ffffff'],
    ['#1D4ED8', '#ffffff'],
    ['#FACC15', '#000000'],
  ])('%s -> %s', (bg, want) => {
    expect(textOn(bg)).toBe(want)
  })

  it('reads at least 4.5:1 on any gray', () => {
    for (let v = 0; v < 256; v += 5) {
      const h = v.toString(16).padStart(2, '0')
      const bg = `#${h}${h}${h}`
      expect(contrast(textOn(bg), bg)).toBeGreaterThanOrEqual(4.5)
    }
  })
})
