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


def write_stack(root, *, leak_employer=False):
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
    for rel in gen.RULE_FILES:
        files[rel] = f"# {MARKER} {Path(rel).stem} rules\n\n{MARKER} rule body\n"
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
