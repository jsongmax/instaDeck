export function evaluatePasswordStrength(pw: string): {
  width: string
  color: string
  text: string
} {
  const n = pw.length
  if (!n) {
    return { width: '0', color: 'var(--line-2)', text: '至少 12 位。' }
  }
  const kinds = [/[a-z]/, /[A-Z]/, /\d/, /[^\w]/].filter((r) => r.test(pw)).length
  const level = n < 12 ? 0 : kinds >= 3 && n >= 16 ? 2 : 1
  return {
    width: `${[28, 62, 100][level]}%`,
    color: ['var(--bad)', 'var(--warn)', 'var(--ok)'][level],
    text: ['太短，至少 12 位', '可以使用，再长一些更安全', '很好'][level],
  }
}

