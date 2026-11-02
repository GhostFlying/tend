import { GroupedUsage } from "./GroupedUsage.tsx";
import type { TokenUsageTotals } from "../api/generated/api.ts";
import { navigate } from "../app/router.ts";
import { ResourceRow } from "../shell/ResourceRow.tsx";
import styles from "./Usage.module.css";

import { field, number } from "./format.ts";
export { field } from "./format.ts";
export function TokenLine({ totals: t }: { totals: TokenUsageTotals }) {
  return (
    <div className={styles.line}>
      <span>
        输入 Token <b>{field(t.input, t.input_known, t.requests)}</b>
        <small>
          缓存读取 {field(t.cache_read, t.cache_read_known, t.requests)}
          {t.cache_write_known > 0 &&
            ` · 缓存写入 ${field(t.cache_write, t.cache_write_known, t.requests)}`}
        </small>
      </span>
      <span>
        输出 Token <b>{field(t.output, t.output_known, t.requests)}</b>
      </span>
    </div>
  );
}

export function UsageSummary({
  totals: t,
  title,
  historical,
}: {
  totals: TokenUsageTotals;
  title: string;
  historical: boolean;
}) {
  return (
    <section className={styles.primary} aria-label={title}>
      <h3>{title} · 已知用量</h3>
      {t.requests ? (
        <>
          <div className={styles.hero}>
            <strong>{t.total_known ? number.format(t.total) : "—"}</strong>
            {t.total_known > 0 && <span>Token</span>}
            {t.total_known > 0 && t.total_known < t.requests && (
              <span className={styles.badge}>部分</span>
            )}
          </div>
          <p className={styles.caption}>
            {number.format(t.total_known)}/{number.format(t.requests)} 个请求提供总量
            {!t.total_known && " · 这些请求未提供总量"}
            {" · "}
            {number.format(t.final)} 个请求已结算
          </p>
          <TokenLine totals={t} />
        </>
      ) : (
        <>
          <p className={styles.emptyTitle}>
            {historical ? "当前筛选下暂无已采集请求" : "此时段暂无可归属请求"}
          </p>
          <p className={styles.caption}>暂无可展示的已知用量，不代表实际消费为零。</p>
        </>
      )}
    </section>
  );
}

export function UsageEntry() {
  return <ResourceRow onOpen={() => navigate({ page: "usage" })}>Token 用量</ResourceRow>;
}

export function UsagePanel({ subject = "" }: { subject?: string }) {
  return <GroupedUsage subject={subject} />;
}
