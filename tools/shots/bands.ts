/** One width the surface is captured at. */
export type Band = {
  /** The name the file carries: the `s`, `m`, `l` or `xl` in `<route>-<band>.png`. */
  readonly name: "s" | "m" | "l" | "xl";
  readonly width: number;
  readonly height: number;
  /** The device pixel ratio; the phone is the only band captured at 3. */
  readonly scale: number;
};

/**
 * BANDS is the four widths a surface PR is reviewed at, narrowest first: the current iPhone Pro
 * Max (440x763 at scale 3, the height Safari leaves), then the tablet, the small desktop and the
 * wide one. Every band is captured at its full viewport.
 */
export const BANDS: readonly Band[] = [
  { name: "s", width: 440, height: 763, scale: 3 },
  { name: "m", width: 768, height: 1024, scale: 1 },
  { name: "l", width: 1024, height: 768, scale: 1 },
  { name: "xl", width: 1280, height: 800, scale: 1 },
];

/** shotName names one shot: `<route>-<band>.png`. */
export function shotName(slug: string, band: Band): string {
  return `${slug}-${band.name}.png`;
}
