"""The generator against a synthetic rule stack: python3 -m unittest discover -s tools/projection"""
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent
SCRIPT = HERE / "gen_projection.py"
sys.path.insert(0, str(HERE))
import gen_projection as gen  # noqa: E402

MARKER = "RULETEXT"
EMPLOYER_HEADING = f"## {MARKER} employer section"


def pack_body(rel, size, extra=""):
    """One synthetic pack: frontmatter, then a body padded to `size` bytes stripped."""
    head = f"# {MARKER} {Path(rel).stem} pack\n\n"
    filler = f"{MARKER} stack guidance\n"
    body = head + extra
    body += filler * max(0, (size - len(body)) // len(filler)) + "\n"
    return f'---\npaths:\n  - "**/*.{Path(rel).stem}"\n---\n\n{body}'


def write_stack(root, *, leak_employer=False, pack_scale=100, pack_line=None):
    per_section = gen.BUILD_DROP_BYTES // len(gen.BUILD_DROPS)
    drops = []
    for heading in gen.BUILD_DROPS:
        line = f"{MARKER} human-in-the-loop filler\n"
        drops.append(f"{heading}\n\n" + line * (per_section // len(line)) + "\n")
    kept = f"## {MARKER} kept\n\n" + "\n".join(gen.MUST_SURVIVE_BUILD.values()) + "\n\n"
    if leak_employer:
        kept += f"{EMPLOYER_HEADING}\n\n"
    base = f"# {MARKER} base\n\n{kept}{''.join(drops)}{gen.ALWAYS_DROPS[0]}\n\n{MARKER} pointer\n"
    files = {
        "base/CLAUDE.md": base,
        "profiles/personal/CLAUDE.md": f"# {MARKER} personal\n",
        "profiles/work/CLAUDE.md": f"# work\n\n{EMPLOYER_HEADING}\n\n{MARKER} employer text\n",
    }
    for rel in gen.RULE_FILES + gen.BUILD_ONLY_RULES:
        files[rel] = f"# {MARKER} {Path(rel).stem} rules\n\n{MARKER} rule body\n"
    per_pack = gen.STACK_PACK_BYTES // len(gen.STACK_PACKS)
    for rel in gen.STACK_PACKS:
        extra = f"{pack_line}\n\n" if pack_line and rel == gen.STACK_PACKS[0] else ""
        files[rel] = pack_body(rel, per_pack * pack_scale // 100, extra)
    for rel, text in files.items():
        path = root / rel
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text)


def run(root, out):
    return subprocess.run([sys.executable, str(SCRIPT), str(root), "--out", str(out)],
                          capture_output=True, text=True)


class GenProjectionTest(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.root = Path(tmp.name) / "stack"
        self.out = Path(tmp.name) / "out"
        self.out.mkdir()

    def test_valid_stack_writes_both_projections(self):
        write_stack(self.root)
        result = run(self.root, self.out)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(sorted(p.name for p in self.out.iterdir()),
                         ["projection-build.md", "projection-plan.md"])
        self.assertNotIn(MARKER, result.stdout + result.stderr)

    def test_build_only_rule_sits_between_last_rule_and_protocol(self):
        write_stack(self.root)
        self.assertEqual(run(self.root, self.out).returncode, 0)
        build = (self.out / "projection-build.md").read_text()
        plan = (self.out / "projection-plan.md").read_text()
        for rel in gen.BUILD_ONLY_RULES:
            heading = gen._own_heading(self.root, rel)
            self.assertGreater(build.index(heading),
                               build.index(gen._own_heading(self.root, gen.RULE_FILES[-1])))
            self.assertLess(build.index(heading), build.index(gen.PROTOCOL_HEADING))
            self.assertNotIn(heading, plan)

    def test_stack_packs_ride_in_the_build_projection_only(self):
        write_stack(self.root)
        result = run(self.root, self.out)
        self.assertEqual(result.returncode, 0, result.stderr)
        build = (self.out / "projection-build.md").read_text()
        plan = (self.out / "projection-plan.md").read_text()
        for rel in gen.STACK_PACKS:
            pack = gen.pack_projection(self.root, rel)
            self.assertIn(pack, build)
            # a pack addresses a reader, not a tool: its frontmatter is not shipped
            self.assertNotIn("paths:", pack)
            self.assertNotIn(f"# {MARKER} {Path(rel).stem} pack", plan)

    def test_stack_packs_sit_between_the_rule_stack_and_the_protocol(self):
        write_stack(self.root)
        self.assertEqual(run(self.root, self.out).returncode, 0)
        build = (self.out / "projection-build.md").read_text()
        last_rule = gen._own_heading(self.root, gen.RULE_FILES[-1])
        evidence = gen._own_heading(self.root, gen.BUILD_ONLY_RULES[0])
        first_pack = f"# {MARKER} {Path(gen.STACK_PACKS[0]).stem} pack"
        self.assertLess(build.index(last_rule), build.index(first_pack))
        self.assertLess(build.index(first_pack), build.index(evidence))
        self.assertLess(build.index(evidence), build.index(gen.PROTOCOL_HEADING))

    def test_generation_fails_when_the_packs_drift_past_the_tolerance(self):
        write_stack(self.root, pack_scale=50)
        result = run(self.root, self.out)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("STACK_PACK_BYTES", result.stderr)
        self.assertEqual(list(self.out.iterdir()), [])
        self.assertNotIn(MARKER, result.stdout + result.stderr)

    def test_generation_fails_when_the_packs_grow_past_the_tolerance(self):
        write_stack(self.root, pack_scale=150)
        result = run(self.root, self.out)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("STACK_PACK_BYTES", result.stderr)
        self.assertEqual(list(self.out.iterdir()), [])

    def test_check_rejects_build_text_missing_a_pack(self):
        write_stack(self.root)
        pack = gen.pack_projection(self.root, gen.STACK_PACKS[0])
        text = gen.build(self.root, trimmed=True).replace(pack, "")
        with self.assertRaises(AssertionError) as caught:
            gen.check(self.root, "build", text, trimmed=True)
        self.assertIn("build projection lost", str(caught.exception))

    def test_check_rejects_a_pack_after_the_protocol(self):
        write_stack(self.root)
        pack = gen.pack_projection(self.root, gen.STACK_PACKS[0])
        text = gen.build(self.root, trimmed=True).replace(pack, "").replace(
            gen.TICKET_SENTINEL, f"{pack}\n\n{gen.TICKET_SENTINEL}")
        with self.assertRaises(AssertionError) as caught:
            gen.check(self.root, "build", text, trimmed=True)
        self.assertIn("is not before the protocol", str(caught.exception))

    def test_check_rejects_plan_text_carrying_a_pack(self):
        write_stack(self.root)
        pack = gen.pack_projection(self.root, gen.STACK_PACKS[0])
        text = gen.build(self.root, trimmed=False).replace(
            gen.TICKET_SENTINEL, f"{pack}\n\n{gen.TICKET_SENTINEL}")
        with self.assertRaises(AssertionError) as caught:
            gen.check(self.root, "plan", text, trimmed=False)
        self.assertIn("plan projection kept", str(caught.exception))

    def test_a_pack_line_naming_a_home_path_is_dropped_not_shipped(self):
        write_stack(self.root, pack_line=f"read ~/dev/{MARKER}/base/CLAUDE.md for the rest")
        result = run(self.root, self.out)
        self.assertEqual(result.returncode, 0, result.stderr)
        build = (self.out / "projection-build.md").read_text()
        self.assertNotIn("~/dev/", build)
        self.assertNotIn(f"read ~/dev/{MARKER}", build)
        # the drop costs the line, not the pack
        self.assertIn(f"# {MARKER} {Path(gen.STACK_PACKS[0]).stem} pack", build)

    def test_check_rejects_a_projection_naming_a_path_outside_the_sandbox(self):
        write_stack(self.root)
        text = gen.build(self.root, trimmed=True).replace(
            gen.TICKET_SENTINEL, f"{MARKER} see /home/someone/tools\n\n{gen.TICKET_SENTINEL}")
        with self.assertRaises(AssertionError) as caught:
            gen.check(self.root, "build", text, trimmed=True)
        self.assertIn("outside the sandbox", str(caught.exception))

    def test_two_runs_over_one_stack_are_byte_identical(self):
        write_stack(self.root)
        again = self.out.parent / "again"
        again.mkdir()
        self.assertEqual(run(self.root, self.out).returncode, 0)
        self.assertEqual(run(self.root, again).returncode, 0)
        for name in ("projection-build.md", "projection-plan.md"):
            self.assertEqual((self.out / name).read_bytes(), (again / name).read_bytes())

    def test_missing_build_only_rule_fails_closed(self):
        write_stack(self.root)
        (self.root / gen.BUILD_ONLY_RULES[0]).unlink()
        result = run(self.root, self.out)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(list(self.out.iterdir()), [])
        self.assertNotIn(MARKER, result.stdout + result.stderr)

    def test_check_rejects_build_text_missing_the_build_only_rule(self):
        write_stack(self.root)
        text = gen.build(self.root, trimmed=True).replace(
            gen._own_heading(self.root, gen.BUILD_ONLY_RULES[0]), "")
        with self.assertRaises(AssertionError) as caught:
            gen.check(self.root, "build", text, trimmed=True)
        self.assertIn("build projection lost", str(caught.exception))

    def test_check_rejects_plan_text_carrying_the_build_only_rule(self):
        write_stack(self.root)
        text = gen.build(self.root, trimmed=False).replace(
            gen.TICKET_SENTINEL, gen._own_heading(self.root, gen.BUILD_ONLY_RULES[0])
            + "\n\n" + gen.TICKET_SENTINEL)
        with self.assertRaises(AssertionError) as caught:
            gen.check(self.root, "plan", text, trimmed=False)
        self.assertIn("plan projection kept", str(caught.exception))

    def test_failed_assertion_writes_nothing_and_prints_no_rule_text(self):
        write_stack(self.root, leak_employer=True)
        result = run(self.root, self.out)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("employer layer leaked: 1 heading(s)", result.stderr)
        self.assertEqual(list(self.out.iterdir()), [])
        self.assertNotIn(MARKER, result.stdout + result.stderr)

    def test_content_after_ticket_is_reported_by_length(self):
        write_stack(self.root)
        text = f"prefix\n\n{gen.TICKET_SENTINEL}\n{MARKER} trailing rule text\n"
        with self.assertRaises(AssertionError) as caught:
            gen.check(self.root, "plan", text, trimmed=False)
        self.assertIn("content after the ticket", str(caught.exception))
        self.assertNotIn(MARKER, str(caught.exception))


if __name__ == "__main__":
    unittest.main()
