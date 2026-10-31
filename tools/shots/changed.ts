// Which page files changed since a base commit, for the default route set.

/** A git command the tool could not run, with what git said. */
export class GitError extends Error {}

/**
 * baseCandidates tries the base as named, then as the remote-tracking branch of
 * that name: a worktree made from `origin/main` often has no local `main`.
 */
export function baseCandidates(base: string): string[] {
  return base.includes("/") ? [base] : [base, `origin/${base}`];
}

/** parseChangedPages reads `git diff --name-only` output. */
export function parseChangedPages(output: string): Set<string> {
  return new Set(
    output
      .split("\n")
      .map((line) => line.trim())
      .filter((line) => line !== ""),
  );
}

/**
 * changedPages lists the repository paths that changed between the merge base
 * of base and HEAD, whether or not base names a local ref.
 */
export function changedPages(repoRoot: string, base: string): Set<string> {
  const ref = baseCandidates(base).find(
    (candidate) =>
      Bun.spawnSync(
        ["git", "-C", repoRoot, "rev-parse", "--verify", "--quiet", `${candidate}^{commit}`],
        { stdout: "ignore", stderr: "ignore" },
      ).exitCode === 0,
  );
  if (ref === undefined) {
    throw new GitError(
      `no commit named ${baseCandidates(base).join(" or ")}; ` +
        "pass --base <ref> naming the branch this change is based on",
    );
  }
  const diff = Bun.spawnSync(["git", "-C", repoRoot, "diff", "--name-only", `${ref}...HEAD`], {
    stdout: "pipe",
    stderr: "pipe",
  });
  if (diff.exitCode !== 0) {
    throw new GitError(`git diff against ${ref} failed: ${diff.stderr.toString().trim()}`);
  }
  return parseChangedPages(diff.stdout.toString());
}
