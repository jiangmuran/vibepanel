/** The code as typed: full-width digits from a Chinese keyboard, spaces. */
export function codeDigits(s: string): string {
  return s.replace(/[\uFF10-\uFF19]/g, (d) => String.fromCharCode(d.charCodeAt(0) - 0xfee0)).replace(/\D/g, '')
}
