/** True when `text` is one code point: ADLaM and Osage letters are surrogate pairs, length 2. */
export function isOneCodePoint(text: string): boolean {
  return /^.$/su.test(text);
}
