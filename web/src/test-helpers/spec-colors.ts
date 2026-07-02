// Display SPEC — color derivations, authored from the published standards, NOT
// from the engine's wire.go. The conformance tests assert the renderer/engine
// against THESE values; a mismatch is a finding, not something to reconcile by
// copying wire.go.
//
// Sources:
//   - 256-color cube + grayscale ramp: xterm's 256colres.pl / the widely
//     documented formula (see `man xterm`, "256color" and ISO-6429 SGR 38;5).
//   - Truecolor: SGR 38;2;R;G;B / 48;2;R;G;B is literally the RGB triple
//     (ITU / ISO-6429 direct-color).
//   - The 16 base colors (indices 0-15, SGR 30-37 / 90-97) are TERMINAL-DEFINED
//     (xterm, VGA, and Windows consoles all differ). They are a palette CHOICE,
//     not a universal spec, so this module does NOT pin them — the conformance
//     tests treat the engine's basic16 palette as a documented choice and only
//     assert internal consistency (distinct, plausible colors), never a
//     "correct" RGB.

/**
 * cube256 returns the standard RGB for an xterm 256-color CUBE index (16-231).
 * The cube is 6×6×6: index i → n=i-16, (r,g,b) base-6 digits of n, each level
 * mapped 0→0 and k>0→55+40·k (so the six levels are 0,95,135,175,215,255).
 * Throws for out-of-range indices so a test can't silently pass on a bad index.
 */
function cube256(index: number): number {
  if (index < 16 || index > 231) {
    throw new RangeError(`cube256: index ${index} outside 16..231`);
  }
  const n = index - 16;
  const level = (k: number): number => (k === 0 ? 0 : 55 + 40 * k);
  const r = level(Math.floor(n / 36));
  const g = level(Math.floor(n / 6) % 6);
  const b = level(n % 6);
  return (r << 16) | (g << 8) | b;
}

/**
 * grayscale256 returns the standard RGB for an xterm 256-color GRAYSCALE index
 * (232-255): v = 8 + 10·(index-232), giving 24 grays from 8 to 238.
 */
function grayscale256(index: number): number {
  if (index < 232 || index > 255) {
    throw new RangeError(`grayscale256: index ${index} outside 232..255`);
  }
  const v = 8 + 10 * (index - 232);
  return (v << 16) | (v << 8) | v;
}

/**
 * standard256 returns the spec RGB for a 256-color index in the cube or
 * grayscale range (16-255). Indices 0-15 are terminal-defined (palette choice)
 * and intentionally NOT covered — call cube256/grayscale256 semantics via this
 * for 16-255 only.
 */
export function standard256(index: number): number {
  if (index >= 16 && index <= 231) {
    return cube256(index);
  }
  if (index >= 232 && index <= 255) {
    return grayscale256(index);
  }
  throw new RangeError(`standard256: index ${index} is terminal-defined (0-15) or invalid`);
}

/** rgb packs an (r,g,b) truecolor triple to 0xRRGGBB, the SGR direct-color value. */
export function rgb(r: number, g: number, b: number): number {
  return (r << 16) | (g << 8) | b;
}

/** hex formats a 0xRRGGBB value as the CSS "#rrggbb" the DOM tiers assert against. */
export function hex(value: number): string {
  return "#" + (value >>> 0).toString(16).padStart(6, "0");
}
