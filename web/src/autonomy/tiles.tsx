import { parsePull } from "../inbox/run";
import { count, isRed, rateOrWords, tileWords, trendWords } from "./format";
import { TrendArrow } from "./marks";
import { useWidth } from "./use-width";
import type { PR, Tile } from "./types";

const LABELS = ["trailing 10", "last 5", "last PR"];

type TilesProps = {
  tiles: Tile[];
  prs: PR[];
  bar: number;
  // Whether the row reaches 10px past the page's text on a phone, as the cards do.
  bleed?: boolean;
  // Words under the number at every width, as the drawing's under-ten-PRs card has them.
  stacked?: boolean;
};

// The row width from which the three tiles sit side by side, and the width from
// which a tile has room for its words beside its number.
const THREE_UP_W = 408;
const BESIDE_W = 640;
// The width a tile's inside needs for its label at 13px; narrower tiles set it at 12px.
const ROOMY_INNER_W = 106;
const TILE_GAP_PX = 12;
const TILE_PAD_PX = 12;

// Tiles are the three windows the rate is read over: the number, the words that
// place it, and which way it is heading. Only the trailing-ten number is coloured,
// and only once its window is full and above the bar, and it then carries the
// words "above 10%" beside it; the arrow has its own words, so colour is never alone.
// The tiles are one height, whatever each holds.
export function Tiles({ tiles, prs, bar, bleed = true, stacked = false }: TilesProps) {
  const { ref, width } = useWidth<HTMLDivElement>();
  const beside = width >= BESIDE_W && !stacked;
  const threeUp = width >= THREE_UP_W;
  const columns = threeUp ? "grid-cols-3" : "grid-cols-1";
  const tileWidth = threeUp ? (width - 2 * TILE_GAP_PX) / 3 : width;
  const roomy = beside || tileWidth - 2 * TILE_PAD_PX >= ROOMY_INNER_W;
  return (
    <div ref={ref} className={bleed ? "max-sm:-mx-2.5" : undefined}>
      {width > 0 && (
        <ul className={`grid auto-rows-fr ${columns} ${beside ? "gap-6" : "gap-3"}`}>
          {tiles.map((tile, i) => (
            <TileCard
              key={LABELS[i]}
              label={LABELS[i] ?? ""}
              tile={tile}
              index={i}
              bar={bar}
              prs={prs}
              beside={beside}
              roomy={roomy}
            />
          ))}
        </ul>
      )}
    </div>
  );
}

type TileCardProps = {
  label: string;
  tile: Tile;
  index: number;
  bar: number;
  prs: PR[];
  beside: boolean;
  roomy: boolean;
};

function TileCard({ label, tile, index, bar, prs, beside, roomy }: TileCardProps) {
  const words = tileWords(tile, index, bar);
  const full = tile.prs.length >= tile.asked;
  const hot = index === 0 && full && isRed(tile.rate_value, bar);
  const trend = trendWords(tile);
  const lastPR = index === 2 ? tile.prs[0] : undefined;
  const number = (
    <span
      className={`font-mono text-hero font-semibold ${hot ? "text-defer" : "text-ink"}`}
      data-rate={tile.rate_value ?? undefined}
    >
      {rateOrWords(tile.rate_value, tile.rate)}
    </span>
  );
  const link = lastPR === undefined ? null : <PRLink pr={lastPR} prs={prs} beside={beside} />;
  const agentLines = (
    <span className="text-micro text-muted">{count(tile.agent_lines)} agent lines</span>
  );
  return (
    <li
      className={`rounded-tile border border-hair bg-card pt-4 pb-3 ${beside ? "px-5" : "px-3"}`}
      data-tile={label}
    >
      {/* One line at every tile width the row allows: three tiles need 120px each. */}
      <h3
        className={`font-mono font-semibold whitespace-nowrap text-ink2 uppercase ${roomy ? "text-label tracking-[1.4px]" : "text-cap tracking-[1px]"}`}
      >
        {label}
      </h3>
      {beside ? (
        <p className="mt-3 flex flex-wrap items-baseline gap-x-3.5 gap-y-1">
          {number}
          {words !== null && <span className="text-body font-semibold text-ink">{words}</span>}
          {link}
        </p>
      ) : (
        <div className="mt-3">
          <p className="break-all">{number}</p>
          {words !== null && <p className="mt-1 text-label font-semibold text-ink">{words}</p>}
          {link !== null && <p className="mt-1">{link}</p>}
        </div>
      )}
      {(trend !== null || index === 2) && (
        <div className="mt-2 flex items-start gap-2 text-label">
          {trend !== null && (
            <span className="mt-[3px] shrink-0">
              <TrendArrow direction={tile.trend} />
            </span>
          )}
          {beside ? (
            <p className="min-w-0">
              {trend !== null && <span className="font-semibold text-ink">{trend}</span>}
              {index === 2 && (
                <>
                  {trend !== null && " "}
                  <span className={trend === null ? "" : "ml-2"}>{agentLines}</span>
                </>
              )}
            </p>
          ) : (
            <div className="min-w-0">
              {trend !== null && <TrendLines words={trend} />}
              {index === 2 && <p>{agentLines}</p>}
            </div>
          )}
        </div>
      )}
    </li>
  );
}

// TrendLines sets a tile's arrow words over two lines where the tile is narrow:
// the direction, then "from 14.1%".
function TrendLines({ words }: { words: string }) {
  const space = words.indexOf(" ");
  if (space < 0) {
    return <p className="font-semibold text-ink">{words}</p>;
  }
  return (
    <p className="font-semibold text-ink">
      <span className="block">{words.slice(0, space)}</span>
      <span className="block">{words.slice(space + 1)}</span>
    </p>
  );
}

// PRLink opens the PR the last-PR tile is about, as a 44px target; the PR's
// address is checked before it is a link, and a PR the list does not carry is
// plain text.
function PRLink({ pr, prs, beside }: { pr: number; prs: PR[]; beside: boolean }) {
  const url = prs.find((entry) => entry.pr === pr)?.pr_url ?? "";
  if (parsePull(url) === null) {
    return <span className="text-body font-semibold text-ink2">#{pr}</span>;
  }
  return (
    <a
      href={url}
      target="_blank"
      rel="noopener noreferrer"
      className={`inline-flex min-h-11 min-w-11 items-center justify-center rounded-control text-body font-semibold text-accent underline ${beside ? "-my-3 ml-auto" : "-ml-1"}`}
    >
      #{pr}
      <span className="sr-only"> (opens in a new tab)</span>
    </a>
  );
}
