const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const { runTests } = require("../../internal/testkit/impact/web.cjs");

function execution(args, web, file) {
  const output = args.find((arg) => arg.startsWith("--outputFile="));
  if (output) fs.writeFileSync(output.slice(13), JSON.stringify({ success: true, testResults: [{ name: path.join(web, file), assertionResults: [{ status: "passed" }] }] }));
}

test("Web graph selection executes discovered files without an empty-match bypass", () => {
  const commands = [];
  const web = path.resolve("web");
  const result = runTests({ Full: false, Base: "a".repeat(40) }, { web, execute: (_, args) => {
    commands.push(args);
    const json = args.find((arg) => arg.startsWith("--json="));
    if (json) fs.writeFileSync(json.slice(7), JSON.stringify([{ file: path.join(web, "src/a.test.ts") }]));
    execution(args, web, "src/a.test.ts");
  } });
  assert.deepEqual(result.Files, ["src/a.test.ts"]);
  assert.deepEqual(commands[1].slice(0, 4), ["exec", "vitest", "run", "src/a.test.ts"]);
  assert.equal(result.Completed, true);
  assert.deepEqual(result.ExecutedFiles, result.Files);
  assert.equal(commands.flat().includes("--passWithNoTests"), false);
});

test("an empty dependency graph widens explicitly and an empty full inventory fails", () => {
  const commands = [];
  const web = path.resolve("web");
  let emptyFull = false;
  const execute = (_, args) => {
    commands.push(args);
    const json = args.find((arg) => arg.startsWith("--json="));
    if (json) fs.writeFileSync(json.slice(7), JSON.stringify(args.includes("--changed") || emptyFull ? [] : [{ file: path.join(web, "src/b.test.ts") }]));
    execution(args, web, "src/b.test.ts");
  };
  const result = runTests({ Full: false, Base: "b".repeat(40) }, { web, execute });
  assert.match(result.Reason, /widened/);
  assert.equal(commands.length, 3);
  emptyFull = true;
  assert.throws(() => runTests({ Full: false, Base: "b".repeat(40) }, { web, execute }), /Empty/);
});

test("discovery alone and an execution report missing the planned file cannot pass", () => {
  const web = path.resolve("web");
  for (const missing of [true, false]) {
    assert.throws(() => runTests({ Full: true }, { web, execute: (_, args) => {
      const json = args.find((arg) => arg.startsWith("--json="));
      if (json) fs.writeFileSync(json.slice(7), JSON.stringify([{ file: path.join(web, "src/required.test.ts") }]));
      if (!missing) execution(args, web, "src/other.test.ts");
    } }), /no execution report|did not execute/);
  }
});

test("Web failures and malformed inventories cannot become green results", () => {
  const web = path.resolve("web");
  for (const invalid of [null, [{ file: "../outside" }], [{ file: path.join(web, "a.test.ts"), projectName: "unknown" }]]) {
    assert.throws(() => runTests({ Full: true }, { web, execute: (_, args) => {
      const json = args.find((arg) => arg.startsWith("--json="));
      fs.writeFileSync(json.slice(7), JSON.stringify(invalid));
    } }));
  }
  assert.throws(() => runTests({ Full: true }, { web, execute() { throw Error("actual failing test"); } }), /failing/);
});
