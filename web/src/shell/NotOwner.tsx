// What a device that tend does not take as the owner's sees (D49, D56).
import { Button } from "../ui/Button.tsx";
import styles from "./NotOwner.module.css";

export function NotOwner({ reason, onRetry }: { reason: string; onRetry: () => void }) {
  return (
    <main className={styles.page}>
      <div className={styles.card}>
        <div className={styles.logo} aria-hidden>
          t
        </div>
        <h1>这台设备不是 owner 的</h1>
        <p>
          tend 按 tailnet 身份认人：只有服务端配置的 owner 的设备才能使用。这台设备登录的 tailnet
          账号不在 <code>owners</code> 里，也没有经 <code>owner_nodes</code> 映射到 owner。
        </p>
        <p>换一台 owner 的设备打开，或者请 owner 修改服务端配置后重试。</p>
        <p className={styles.reason}>tend 的回答：{reason}</p>
        <Button type="button" variant="secondary" size="s" onClick={onRetry}>
          重试
        </Button>
      </div>
    </main>
  );
}
