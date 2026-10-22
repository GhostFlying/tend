import { useEffect, useState } from "react";
import type { ApiClient } from "./api/client.ts";
import type { SendQueue } from "./queue/sendQueue.ts";
import { NotOwner } from "./shell/NotOwner.tsx";
import { Shell } from "./shell/Shell.tsx";
import { useStore } from "./state/store.ts";

export function App({ client, queue }: { client: ApiClient; queue: Promise<SendQueue | null> }) {
  const connection = useStore(client.connection);
  const [ready, setReady] = useState<SendQueue | null>(null);

  useEffect(() => {
    let live = true;
    void queue.then((q) => live && setReady(q));
    return () => {
      live = false;
    };
  }, [queue]);

  if (connection.status === "not-owner") {
    return (
      <NotOwner reason={connection.reason} onRetry={() => void client.connect().catch(() => {})} />
    );
  }
  return <Shell connection={connection} queue={ready} />;
}
