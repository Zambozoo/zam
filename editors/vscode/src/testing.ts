import { spawn } from "child_process";
import { promises as fs } from "fs";
import * as os from "os";
import * as path from "path";
import {
  CancellationToken,
  debug,
  Disposable,
  FileCoverage,
  Location,
  Position,
  Range,
  StatementCoverage,
  TestController,
  TestItem,
  TestMessage,
  TestRun,
  TestRunProfileKind,
  TestRunRequest,
  tests,
  Uri,
  workspace,
} from "vscode";
import { LanguageClient } from "vscode-languageclient/node";

// TestInfo is a test of a file as the language server answers it: its
// qualified name, or empty for every test of the file, and where it is
// declared.
interface TestInfo {
  name: string;
  range: { start: { line: number; character: number }; end: { line: number; character: number } };
}

// names holds the qualified name of each test item below a file.
const names = new WeakMap<TestItem, string>();

// details holds the lines of each file's coverage, for VS Code to show.
const details = new WeakMap<FileCoverage, StatementCoverage[]>();

// testPattern picks the tests of names by their qualified names, or every
// test for none.
export function testPattern(...picked: string[]): string {
  const escaped = picked.filter((name) => name).map((name) => name.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"));
  return escaped.length === 0 ? "" : "^(" + escaped.join("|") + ")$";
}

// createTesting lists the workspace's test files and their tests in the
// Testing view, and runs, covers and debugs them with zam.
export function createTesting(client: LanguageClient, zamPath: () => string): Disposable {
  const controller = tests.createTestController("zam", "Zam");

  const addFile = (uri: Uri): TestItem => {
    const existing = controller.items.get(uri.toString());
    if (existing) {
      return existing;
    }
    const item = controller.createTestItem(uri.toString(), workspace.asRelativePath(uri), uri);
    item.canResolveChildren = true;
    controller.items.add(item);
    return item;
  };

  const resolveFile = async (file: TestItem): Promise<void> => {
    const found = await client.sendRequest<TestInfo[]>("zam/tests", { uri: file.uri!.toString() });
    const children: TestItem[] = [];
    for (const test of found) {
      const range = new Range(test.range.start.line, test.range.start.character, test.range.end.line, test.range.end.character);
      if (!test.name) {
        file.range = range;
        continue;
      }
      const child = controller.createTestItem(file.id + "#" + test.name, test.name.slice(test.name.lastIndexOf(".") + 1), file.uri);
      child.range = range;
      names.set(child, test.name);
      children.push(child);
    }
    file.children.replace(children);
  };

  controller.resolveHandler = async (item) => {
    if (item) {
      await resolveFile(item);
      return;
    }
    for (const uri of await workspace.findFiles("**/*.test.zam")) {
      addFile(uri);
    }
  };

  const watcher = workspace.createFileSystemWatcher("**/*.test.zam");
  watcher.onDidCreate((uri) => addFile(uri));
  watcher.onDidChange(async (uri) => {
    const file = controller.items.get(uri.toString());
    if (file && file.children.size > 0) {
      await resolveFile(file);
    }
  });
  watcher.onDidDelete((uri) => controller.items.delete(uri.toString()));

  const run = async (request: TestRunRequest, token: CancellationToken, cover: boolean): Promise<void> => {
    const testRun = controller.createTestRun(request);
    const coverage = new Map<string, Map<number, boolean>>();
    try {
      for (const [file, picked] of await groupByFile(controller, request, resolveFile)) {
        if (token.isCancellationRequested) {
          break;
        }
        await runFile(testRun, zamPath(), file, picked, cover ? coverage : undefined, token);
      }
      for (const [file, lines] of coverage) {
        const statements = [...lines].map(([line, hit]) => new StatementCoverage(hit ? 1 : 0, new Position(line - 1, 0)));
        const fileCoverage = FileCoverage.fromDetails(Uri.file(file), statements);
        details.set(fileCoverage, statements);
        testRun.addCoverage(fileCoverage);
      }
    } finally {
      testRun.end();
    }
  };

  controller.createRunProfile("Run", TestRunProfileKind.Run, (request, token) => run(request, token, false), true);
  const coverProfile = controller.createRunProfile("Run with Coverage", TestRunProfileKind.Coverage, (request, token) => run(request, token, true), true);
  coverProfile.loadDetailedCoverage = async (_run, fileCoverage) => details.get(fileCoverage) ?? [];
  controller.createRunProfile(
    "Debug",
    TestRunProfileKind.Debug,
    async (request) => {
      const item = request.include?.[0];
      if (!item?.uri) {
        return;
      }
      await debug.startDebugging(workspace.getWorkspaceFolder(item.uri), {
        type: "zam",
        request: "launch",
        name: "Debug " + item.label,
        program: item.uri.fsPath,
        test: testPattern(names.get(item) ?? ""),
      });
    },
    true,
  );

  return Disposable.from(controller, watcher);
}

// groupByFile answers the tests request picks, by the file each is in: every
// test of a file picked whole, or the tests picked of it.
async function groupByFile(
  controller: TestController,
  request: TestRunRequest,
  resolveFile: (file: TestItem) => Promise<void>,
): Promise<Map<TestItem, TestItem[]>> {
  const excluded = new Set(request.exclude ?? []);
  const groups = new Map<TestItem, TestItem[]>();
  const included: TestItem[] = [];
  if (request.include) {
    included.push(...request.include);
  } else {
    controller.items.forEach((item) => included.push(item));
  }
  for (const item of included) {
    if (excluded.has(item)) {
      continue;
    }
    if (item.parent) {
      groups.set(item.parent, [...(groups.get(item.parent) ?? []), item]);
      continue;
    }
    if (item.children.size === 0) {
      await resolveFile(item);
    }
    const children: TestItem[] = [];
    item.children.forEach((child) => {
      if (!excluded.has(child)) {
        children.push(child);
      }
    });
    groups.set(item, children);
  }
  return groups;
}

// runFile runs the picked tests of file with zam test, reporting each as its
// output says it went, and folds the lines it covered into coverage when
// there is one.
async function runFile(
  testRun: TestRun,
  zam: string,
  file: TestItem,
  picked: TestItem[],
  coverage: Map<string, Map<number, boolean>> | undefined,
  token: CancellationToken,
): Promise<void> {
  const byName = new Map<string, TestItem>();
  for (const test of picked) {
    byName.set(names.get(test)!, test);
    testRun.enqueued(test);
  }
  const whole = picked.length === file.children.size;
  const args = ["test"];
  if (!whole) {
    args.push("--run", testPattern(...byName.keys()));
  }
  let profile: string | undefined;
  if (coverage) {
    profile = path.join(await fs.mkdtemp(path.join(os.tmpdir(), "zam-cover-")), "lcov.info");
    args.push("--coverprofile", profile);
  }
  const filePath = file.uri!.fsPath;
  args.push(filePath);

  const ended = new Set<TestItem>();
  let current: TestItem | undefined;
  let messages: TestMessage[] = [];
  let output: string[] = [];
  let started = 0;

  const line = (text: string): void => {
    testRun.appendOutput(text + "\r\n", undefined, current);
    let match: RegExpMatchArray | null;
    if ((match = text.match(/^=== RUN {3}(\S+)$/))) {
      current = byName.get(match[1]);
      messages = [];
      output = [];
      started = Date.now();
      if (current) {
        testRun.started(current);
      }
    } else if ((match = text.match(/^--- (PASS|FAIL): (\S+)$/))) {
      const test = byName.get(match[2]);
      if (test) {
        if (match[1] === "PASS") {
          testRun.passed(test, Date.now() - started);
        } else {
          testRun.failed(test, messages.length > 0 ? messages : [new TestMessage(output.join("\n") || "failed")], Date.now() - started);
        }
        ended.add(test);
      }
      current = undefined;
    } else if ((match = text.match(/^ {4}(\S+\.test\.zam):(\d+): (.*)$/))) {
      const message = new TestMessage(match[3]);
      if (match[1] === path.basename(filePath)) {
        message.location = new Location(file.uri!, new Position(Number(match[2]) - 1, 0));
      }
      messages.push(message);
    } else if (current) {
      output.push(text);
    }
  };

  const code = await new Promise<number | null>((resolve) => {
    const child = spawn(zam, args, { cwd: path.dirname(filePath) });
    const cancel = token.onCancellationRequested(() => child.kill());
    let pending = "";
    const read = (chunk: Buffer): void => {
      const lines = (pending + chunk.toString()).split(/\r?\n/);
      pending = lines.pop() ?? "";
      lines.forEach(line);
    };
    child.stdout.on("data", read);
    child.stderr.on("data", read);
    child.on("error", (err) => {
      line(err.message);
      cancel.dispose();
      resolve(null);
    });
    child.on("close", (status) => {
      if (pending) {
        line(pending);
      }
      cancel.dispose();
      resolve(status);
    });
  });

  for (const test of picked) {
    if (ended.has(test)) {
      continue;
    }
    if (test === current) {
      testRun.errored(test, new TestMessage(output.join("\n") || "the test's process ended with status " + code));
    } else if (token.isCancellationRequested) {
      testRun.skipped(test);
    } else if (code === 0) {
      testRun.skipped(test);
    } else {
      testRun.errored(test, new TestMessage("the test did not run: zam test ended with status " + code));
    }
  }

  if (coverage && profile) {
    try {
      mergeLCOV(coverage, await fs.readFile(profile, "utf8"));
    } catch {
      // A run that did not get as far as writing coverage covers nothing.
    } finally {
      await fs.rm(path.dirname(profile), { recursive: true, force: true });
    }
  }
}

// mergeLCOV folds the lines an LCOV report says ran into coverage, a line run
// when any report says it ran.
export function mergeLCOV(coverage: Map<string, Map<number, boolean>>, lcov: string): void {
  let lines: Map<number, boolean> | undefined;
  for (const text of lcov.split(/\r?\n/)) {
    if (text.startsWith("SF:")) {
      const file = text.slice(3);
      lines = coverage.get(file) ?? new Map();
      coverage.set(file, lines);
    } else if (text.startsWith("DA:") && lines) {
      const [line, hits] = text.slice(3).split(",").map(Number);
      lines.set(line, (lines.get(line) ?? false) || hits > 0);
    } else if (text === "end_of_record") {
      lines = undefined;
    }
  }
}
