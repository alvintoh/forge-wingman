#!/usr/bin/env python3
"""Project the layered rule stack into the two agent prompts FR-19 requires.

FR-19 says the projections are GENERATED and never hand-maintained, so this
script is the single mechanism: a full projection for the plan agent, a trimmed
one for the build agent. The trim rule is not a judgement call — the build agent
is UNATTENDED, so every rule assuming a human in the loop is dead weight.

The stack lives in the owner's personal config repo, which is private and is
NEVER named here: pass its root as argv[1], or set RULE_STACK_ROOT.

    python3 gen_projection.py <rule-stack-root> [--out DIR]

Every claim this file makes about the output is asserted against the EMITTED
text, not against the intent — the same discipline as the diagram generators.
"""
import os
import re
import sys
from pathlib import Path

# ---- the stack, in load order. Order is load-bearing: see ORDER below. -------
LAYERS = ("base/CLAUDE.md", "profiles/personal/CLAUDE.md", "profiles/work/CLAUDE.md")
EMPLOYER_LAYER = "profiles/work/CLAUDE.md"   # dropped from BOTH projections
RULE_FILES = ("base/rules/code-comments.md", "base/rules/testing.md",
              "base/rules/schema-changes.md")

# ---- FR-19's trim rule, as section-heading prefixes -------------------------
# Each entry is one of the five human-in-the-loop concerns FR-19 names. The
# measured total is asserted below, so a stack edit that moves one shows up as
# a failure rather than as a silently smaller prompt.
BUILD_DROPS = (
    "## Code Review Scope",                    # reviewing a PR conversationally
    "## Gate Decisions With One-Tap Choices",  # gating decisions
    "## Presenting Information",               # presenting information as tables
    "## Proactively Offer to Codify Learnings",# offering to codify learnings
    "## Delegating to Agents",                 # delegating to agents
)
BUILD_DROP_BYTES = 47256          # measured 2026-09-22; PRD states "roughly 47 KB"
BUILD_DROP_TOLERANCE = 6000       # a stack edit inside this is drift, not a defect

# ---- the projection must not send the agent OUT of its sandbox --------------
# Dropped from EVERY projection, trimmed or not. The stack's closing section
# describes the path-scoped rules as living in a config directory and instructs
# the reader to go and open them — which is FALSE of a projection, because their
# content is inlined below by RULE_FILES. Left in, a compliant agent obeys the
# instruction, the harness refuses the read as outside the working directory, and
# the run can DIE there: verified 2026-09-22, one cell of four terminated on
# `permission requested: external_directory (~/.claude/rules/*); auto-rejecting`
# having done no work, which reads as model variance rather than as a defect.
# A projection has to be self-contained; a pointer to a path the agent cannot
# reach is worse than no pointer, and here it is also redundant.
ALWAYS_DROPS = (
    "## Rules that load on matching files",
)

PLAN_DROP_RULES = ("base/rules/code-comments.md",)  # only a writer needs these

# ---- FR-10 / FR-9 / trap 3 must SURVIVE the build trim ----------------------
# The PRD names this as the probe's real risk: cutting the largest
# human-in-the-loop section may take the instinct to STOP AT A FORK with it.
MUST_SURVIVE_BUILD = {
    "FR-10 two defensible options -> the owner's call":
        "say it is the requirement owner's call rather than presenting a preference",
    "FR-10 an unproven path is gated, not ground through":
        "gate it rather than grinding on the driver",
    "FR-9 an unconfirmable value ships as a marked TODO, not as fact":
        "ship it as a clearly-marked **TODO/assumption**",
    "trap 3 staging is by explicit path":
        "Stage by explicit PATH",
}

# The runner replaces this and reads projection-build.md by name: internal/runner/prompt.go
# and internal/runner/projection.go must change with them.
TICKET_SENTINEL = "<<<TICKET>>>"

# ---- how the UNATTENDED agent resolves conflicts the rules leave open --------
PROTOCOL_HEADING = "# How to apply the rules above"
PROTOCOL = f"""{PROTOCOL_HEADING}

1. An unverifiable fact is TODO-and-CONTINUE, never stop-the-slice. Mark it at the exact line, record it for the PR, and complete every other item in the ticket.
2. An explicit ticket instruction OUTRANKS a style convention. Where a convention argues against a specified change, make the change and note the tension for the reviewer.
"""


def sections(text):
    """Split a layer into {heading: block}, preserving the preamble under ''."""
    parts = re.split(r"^(## .*)$", text, flags=re.M)
    out = {"": parts[0]}
    for i in range(1, len(parts), 2):
        out[parts[i].strip()] = parts[i] + parts[i + 1]
    return out


def strip_sections(text, prefixes):
    """Remove whole ## sections whose heading starts with any prefix."""
    kept, dropped = [], 0
    for head, block in sections(text).items():
        if head and head.startswith(prefixes):
            dropped += len(block.encode())
            continue
        kept.append(block)
    return "".join(kept), dropped


def build(root, *, trimmed):
    """Assemble one projection. Invariant rules FIRST, ticket LAST."""
    chunks = []
    for rel in LAYERS:
        if rel == EMPLOYER_LAYER:
            continue                      # both projections drop it entirely
        text = (root / rel).read_text()
        if trimmed and rel == "base/CLAUDE.md":
            text, dropped = strip_sections(text, BUILD_DROPS)
            low, high = (BUILD_DROP_BYTES - BUILD_DROP_TOLERANCE,
                         BUILD_DROP_BYTES + BUILD_DROP_TOLERANCE)
            assert low <= dropped <= high, (
                "the build trim no longer removes what FR-19 measured; a named "
                f"section was probably renamed. dropped={dropped}, expected~{BUILD_DROP_BYTES}")
        if rel == "base/CLAUDE.md":
            # AFTER the measured trim, so ALWAYS_DROPS is never counted in
            # BUILD_DROP_BYTES — the two drops answer different questions.
            text, _ = strip_sections(text, ALWAYS_DROPS)
        chunks.append(text)

    for rel in RULE_FILES:
        if not trimmed and rel in PLAN_DROP_RULES:
            continue                      # plan agent writes no code
        chunks.append((root / rel).read_text())

    if trimmed:
        chunks.append(PROTOCOL)           # after the rules it governs

    # ORDER: the invariant stack is the cacheable prefix, so the ticket is last.
    return "\n\n".join(chunks) + f"\n\n# The ticket\n\n{TICKET_SENTINEL}\n"


def check(root, name, text, *, trimmed):
    """Assert against the EMITTED text, never against what we meant to emit."""
    # the employer layer contributes nothing — tested by its own headings, so no
    # employer-identifying string is ever written into this public file
    employer = sections((root / EMPLOYER_LAYER).read_text())
    leaked = [h for h in employer if h and h in text]
    assert not leaked, f"{name}: employer layer leaked: {leaked}"

    # the ticket is LAST, or the cacheable prefix is broken
    assert text.count(TICKET_SENTINEL) == 1, f"{name}: ticket sentinel not unique"
    # no projection may instruct the agent to read a path outside its sandbox
    for prefix in ALWAYS_DROPS:
        assert prefix not in text, f"{name}: {prefix} survived — see ALWAYS_DROPS"
    assert "~/.claude/rules/" not in text, \
        f"{name}: points the agent at a config path it cannot reach"
    tail = text.split(TICKET_SENTINEL)[1].strip()
    assert tail == "", f"{name}: content after the ticket breaks the cache prefix: {tail[:60]!r}"

    if trimmed:
        for prefix in BUILD_DROPS:
            assert prefix not in text, f"{name}: {prefix} survived the trim"
        for label, phrase in MUST_SURVIVE_BUILD.items():
            assert phrase in text, f"{name}: LOST — {label}"
        # the build agent writes code, so it KEEPS comment conventions
        assert _own_heading(root, PLAN_DROP_RULES[0]) in text, \
            f"{name}: build projection lost comment conventions"
        # the protocol sits between the last rule and the ticket, whole
        for line in PROTOCOL.splitlines()[2:]:
            assert line in text, f"{name}: protocol line missing: {line[:40]!r}"
        head = text.index(PROTOCOL_HEADING)
        last_rule = (root / RULE_FILES[-1]).read_text().strip()
        assert text.rindex(last_rule) < head < text.index(TICKET_SENTINEL), \
            f"{name}: protocol is not between the rule stack and the ticket"
    else:
        # the plan projection keeps the human-in-the-loop rules
        for prefix in BUILD_DROPS:
            assert prefix in text, f"{name}: plan projection is missing {prefix}"
        # Checked by that file's OWN H1, the way the employer layer is checked
        # above. A substring like "Comment density" also appears in the base
        # layer's POINTER to the rule file, so it cannot tell the pointer from
        # the content — the first version of this assertion failed on exactly
        # that and reported a correct output as broken.
        h1 = _own_heading(root, PLAN_DROP_RULES[0])
        assert h1 not in text, f"{name}: plan projection kept comment conventions"
        # the protocol serves the unattended build agent only
        assert PROTOCOL_HEADING not in text, f"{name}: plan projection kept the protocol"

def _own_heading(root, rel):
    """The rule file's first H1 — a marker unique to it across the stack."""
    for line in (root / rel).read_text().splitlines():
        if line.startswith("# "):
            return line
    raise AssertionError(f"{rel} has no H1 to identify it by")


def main():
    # Every check here is an assert, which python -O strips; refuse rather than emit unchecked.
    if not __debug__:
        sys.exit("refusing to run under python -O: the projection's checks are asserts")
    # --out is REQUIRED, deliberately. Defaulting to "." would write ~350 KB of
    # generated rule stack into whatever repo you happen to be standing in, and
    # the projections are build output that no repo should hold.
    assert "--out" in sys.argv, "pass --out DIR; the projections are build output, not repo content"
    i = sys.argv.index("--out")
    out = Path(sys.argv[i + 1])
    rest = sys.argv[1:i] + sys.argv[i + 2:]
    root = Path(rest[0] if rest else os.environ["RULE_STACK_ROOT"])

    full = build(root, trimmed=False)
    trim = build(root, trimmed=True)
    check(root, "plan", full, trimmed=False)
    check(root, "build", trim, trimmed=True)

    for name, text in (("projection-plan.md", full), ("projection-build.md", trim)):
        (out / name).write_text(text)
        n = len(text.encode())
        print(f"  {name:22s} {n:7d} bytes  ~{n // 4:6d} tokens")
    saved = len(full.encode()) - len(trim.encode())
    print(f"  the build trim removes {saved} bytes (~{saved // 4} tokens) "
          f"= {100 * saved / len(full.encode()):.1f}% of the plan projection")


if __name__ == "__main__":
    main()
