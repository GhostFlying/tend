// Identifiers the client creates. Request IDs follow internal/protocol's
// format: a type prefix and 26 Crockford base32 digits holding 48 bits of
// milliseconds and 80 random bits, the ULID layout.

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ";

/** Returns n random bytes. */
export type RandomBytes = (n: number) => Uint8Array;

export const cryptoRandom: RandomBytes = (n) => crypto.getRandomValues(new Uint8Array(n));

/** Returns a new request ID, such as req_01K8…, valid for the protocol's ValidID. */
export function newRequestId(now: number, random: RandomBytes = cryptoRandom): string {
  let value = BigInt(Math.floor(now)) & 0xffff_ffff_ffffn;
  for (const b of random(10)) {
    value = (value << 8n) | BigInt(b);
  }
  let out = "";
  for (let i = 0; i < 26; i++) {
    out = crockford[Number(value & 31n)] + out;
    value >>= 5n;
  }
  return `req_${out}`;
}

/** Returns a new client ID for POST /api/v1/session: 8 to 64 of [A-Za-z0-9_-]. */
export function newClientId(random: RandomBytes = cryptoRandom): string {
  let out = "web_";
  for (const b of random(20)) {
    out += crockford[b & 31];
  }
  return out;
}
