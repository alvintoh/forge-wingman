import ts from "typescript";
import { readFileSync } from "node:fs";

const exits = new Set([
  ts.SyntaxKind.ReturnStatement,
  ts.SyntaxKind.ThrowStatement,
  ts.SyntaxKind.BreakStatement,
  ts.SyntaxKind.ContinueStatement,
]);

const guards = [];
for (const name of process.argv.slice(1)) {
  let text;
  try {
    text = readFileSync("../" + name, "utf8");
  } catch {
    continue;
  }
  const file = ts.createSourceFile(name, text, ts.ScriptTarget.Latest, true);
  const bytes = (offset) => Buffer.byteLength(text.slice(0, offset));
  const visit = (node) => {
    if (ts.isIfStatement(node) && !node.elseStatement) {
      const body = node.thenStatement;
      const last = ts.isBlock(body) ? body.statements.at(-1) : body;
      if (last && exits.has(last.kind)) {
        const start = node.getStart(file);
        guards.push({
          file: name,
          line: text.slice(0, start).split("\n").length,
          header: text.slice(start, body.getStart(file)).split(/\s+/).filter(Boolean).join(" "),
          cond: [bytes(node.expression.getStart(file)), bytes(node.expression.getEnd())],
        });
      }
    }
    ts.forEachChild(node, visit);
  };
  visit(file);
}
console.log(JSON.stringify(guards));
