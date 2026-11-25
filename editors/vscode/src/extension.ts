import { debug, workspace, DebugAdapterExecutable, ExtensionContext } from "vscode";
import { LanguageClient, LanguageClientOptions, ServerOptions } from "vscode-languageclient/node";
import { createTesting } from "./testing";

let client: LanguageClient | undefined;

function zamPath(): string {
  return workspace.getConfiguration("zam").get<string>("serverPath", "zam");
}

export async function activate(context: ExtensionContext): Promise<void> {
  context.subscriptions.push(
    debug.registerDebugAdapterDescriptorFactory("zam", {
      createDebugAdapterDescriptor: () => new DebugAdapterExecutable(zamPath(), ["debug"]),
    }),
  );

  const serverOptions: ServerOptions = { command: zamPath(), args: ["lsp"] };
  const clientOptions: LanguageClientOptions = {
    documentSelector: [{ scheme: "file", language: "zam" }],
  };

  client = new LanguageClient("zam", "Zam Language Server", serverOptions, clientOptions);
  context.subscriptions.push(client);
  await client.start();
  context.subscriptions.push(createTesting(client, zamPath));
}

export async function deactivate(): Promise<void> {
  await client?.stop();
}
