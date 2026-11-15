import * as path from "path";
import { commands, debug, window, workspace, Uri, DebugAdapterExecutable, ExtensionContext, Terminal } from "vscode";
import { LanguageClient, LanguageClientOptions, ServerOptions } from "vscode-languageclient/node";

let client: LanguageClient | undefined;
let testTerminal: Terminal | undefined;

function zamPath(): string {
  return workspace.getConfiguration("zam").get<string>("serverPath", "zam");
}

// testPattern picks one test by its qualified name, or every test for none.
function testPattern(name: string): string {
  return name ? "^" + name.replace(/[.*+?^${}()|[\]\\]/g, "\\$&") + "$" : "";
}

// quote quotes an argument for a POSIX shell.
function quote(argument: string): string {
  return "'" + argument.replace(/'/g, "'\\''") + "'";
}

function runTest(file: string, name: string): void {
  if (testTerminal === undefined || testTerminal.exitStatus !== undefined) {
    testTerminal = window.createTerminal({ name: "Zam tests", cwd: path.dirname(file) });
  }
  const run = name ? ["--run", quote(testPattern(name))] : [];
  testTerminal.show();
  testTerminal.sendText([quote(zamPath()), "test", ...run, quote(file)].join(" "));
}

async function debugTest(file: string, name: string): Promise<void> {
  await debug.startDebugging(workspace.getWorkspaceFolder(Uri.file(file)), {
    type: "zam",
    request: "launch",
    name: "Debug " + (name || path.basename(file)),
    program: file,
    test: testPattern(name),
  });
}

export async function activate(context: ExtensionContext): Promise<void> {
  context.subscriptions.push(
    debug.registerDebugAdapterDescriptorFactory("zam", {
      createDebugAdapterDescriptor: () => new DebugAdapterExecutable(zamPath(), ["debug"]),
    }),
    commands.registerCommand("zam.runTest", runTest),
    commands.registerCommand("zam.debugTest", debugTest),
  );

  const serverOptions: ServerOptions = { command: zamPath(), args: ["lsp"] };
  const clientOptions: LanguageClientOptions = {
    documentSelector: [{ scheme: "file", language: "zam" }],
  };

  client = new LanguageClient("zam", "Zam Language Server", serverOptions, clientOptions);
  context.subscriptions.push(client);
  await client.start();
}

export async function deactivate(): Promise<void> {
  await client?.stop();
}
