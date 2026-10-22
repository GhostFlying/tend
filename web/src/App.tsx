import { useEffect, useState } from "react";
import type { ApiClient, Connection } from "./api/client.ts";
import type { LiveData } from "./live/data.ts";
import type { SendQueue } from "./queue/sendQueue.ts";
import { NotOwner } from "./shell/NotOwner.tsx";
import { Shell } from "./shell/Shell.tsx";
import { useStore, type Readable } from "./state/store.ts";

export function App({
  client,
  live,
  connection: link,
  queue,
}: {
  client: ApiClient;
  live: LiveData;
  connection: Readable<Connection>;
  queue: Promise<SendQueue | null>;
}) {
  const connection = useStore(link);
  const [ready, setReady] = useState<SendQueue | null>(null);

  useEffect(() => {
    let alive = true;
    void queue.then((q) => alive && setReady(q));
    return () => {
      alive = false;
    };
  }, [queue]);

  if (connection.status === "not-owner") {
    return (
      <NotOwner reason={connection.reason} onRetry={() => void client.connect().catch(() => {})} />
    );
  }
  return <Shell client={client} live={live} connection={connection} queue={ready} />;
}
