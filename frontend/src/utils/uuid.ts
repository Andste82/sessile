// A UUID v4 for ids the client chooses (agent settings items, §6). Not
// crypto.randomUUID: that only exists in a secure context, and sessile is
// routinely reached over plain HTTP on a LAN; getRandomValues is available
// everywhere.
export function uuidv4(): string {
  const b = crypto.getRandomValues(new Uint8Array(16))
  b[6] = (b[6] & 0x0f) | 0x40
  b[8] = (b[8] & 0x3f) | 0x80
  const h = Array.from(b, (x) => x.toString(16).padStart(2, '0')).join('')
  return `${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20)}`
}
