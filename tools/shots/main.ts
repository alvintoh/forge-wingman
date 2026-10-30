import { resolve } from "node:path";
import { capture, launchChromium } from "./capture";
import { runShots } from "./run";

const ROOT = resolve(import.meta.dir, "../..");

process.exit(
  await runShots(Bun.argv.slice(2), {
    root: ROOT,
    launch: launchChromium,
    capture,
    git: (args) => {
      const result = Bun.spawnSync(["git", "-C", ROOT, ...args], {
        stdout: "pipe",
        stderr: "pipe",
      });
      return result.exitCode === 0 ? result.stdout.toString() : null;
    },
    log: (line) => console.log(line),
    error: (line) => console.error(line),
  }),
);
