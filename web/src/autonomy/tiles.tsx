import { parsePull } from "../inbox/run";
import { captionClass } from "./chart-card";
import { count, isRed, rateOrWords, tileWords, trendWords } from "./format";
import { TrendArrow } from "./marks";
import type { PR, Tile } from "./types";

const LABELS = ["trailing 10", "last 5", "last PR"];

type TilesProps = {
  tiles: Tile[];
  prs: PR[];
  bar: number;
};

// Tiles are the three windows the rate is read over: the number, the words that
// place it, and which way it is heading. Only the trailing-ten number is coloured,
// and only once its window is full and above the bar, and it then carries the
// words "above 10%" beside it; the arrow has its own words, so colour is never alone.
export function Tiles({ tiles, prs, bar }: TilesProps) {
  return (
    <div className="@container max-sm:-mx-2.5">
      <ul className="grid grid-cols-1 gap-3 @[24rem]:grid-cols-3 @[40rem]:gap-6">
        {tiles.map((tile, i) => (
          <TileCard
            key={LABELS[i]}
            label={LABELS[i] ?? ""}
            tile={tile}
            index={i}
            bar={bar}
            prs={prs}
          />
        ))}
      </ul>
    </div>
  );
}

type TileCardProps = {
  label: string;
  tile: Tile;
  index: number;
  bar: number;
  prs: PR[];
};

function TileCard({ label, tile, index, bar, prs }: TileCardProps) {
  const words = tileWords(tile, index, bar);
  const full = tile.prs.length >= tile.asked;
  const hot = index === 0 && full && isRed(tile.rate_value, bar);
  const trend = trendWords(tile);
  const lastPR = index === 2 ? tile.prs[0] : undefined;
  return (
    <li
      className="rounded-tile border border-hair bg-card px-3 pt-4 pb-3 @[40rem]:px-5"
      data-tile={label}
    >
      <h3 className={captionClass}>{label}</h3>
      <p className="mt-3 flex flex-wrap items-baseline gap-x-3.5 gap-y-1">
        <span
          className={`font-mono text-hero font-semibold ${hot ? "text-defer" : "text-ink"}`}
          data-rate={tile.rate_value ?? undefined}
        >
          {rateOrWords(tile.rate_value, tile.rate)}
        </span>
        {words !== null && (
          <span className="text-label font-semibold text-ink @[40rem]:text-body">{words}</span>
        )}
        {lastPR !== undefined && <PRLink pr={lastPR} prs={prs} />}
      </p>
      {(trend !== null || index === 2) && (
        <p className="mt-2 flex flex-wrap items-center gap-x-2 gap-y-0.5 text-label">
          {trend !== null && (
            <>
              <TrendArrow direction={tile.trend} />
              <span className="font-semibold text-ink">{trend}</span>
            </>
          )}
          {index === 2 && (
            <span className="text-micro text-muted">{count(tile.agent_lines)} agent lines</span>
          )}
        </p>
      )}
    </li>
  );
}

// PRLink opens the PR the last-PR tile is about, as a 44px target; the PR's
// address is checked before it is a link, and a PR the list does not carry is
// plain text.
function PRLink({ pr, prs }: { pr: number; prs: PR[] }) {
  const url = prs.find((entry) => entry.pr === pr)?.pr_url ?? "";
  if (parsePull(url) === null) {
    return <span className="text-body font-semibold text-ink2">#{pr}</span>;
  }
  return (
    <a
      href={url}
      target="_blank"
      rel="noopener noreferrer"
      className="-my-3 ml-auto inline-flex min-h-11 min-w-11 items-center justify-center rounded-control text-body font-semibold text-accent underline"
    >
      #{pr}
      <span className="sr-only"> (opens in a new tab)</span>
    </a>
  );
}
