import { useEffect, useState } from "react";
import { useServices } from "../app/services.tsx";
import type {
  ExtensionBinding,
  ExtensionCombination,
  ExtensionRelease,
  ExtensionReleasePage,
  ExtensionState,
  ExtensionStatus,
} from "../api/generated/api.ts";
import { Button, EmptyState, Field, Input, Section, Select } from "../ui/index.ts";
import styles from "./Extensions.module.css";

function Binding({ value }: { value: ExtensionBinding }) {
  return (
    <details>
      <summary>
        {value.category_id || `${value.agent?.execution_environment_id}/${value.agent?.local_id}`} ·
        revision {value.revision}
      </summary>
      <ul>
        {(value.choices ?? []).map((choice) => (
          <li key={`${choice.kind}:${choice.id}`}>
            {choice.kind} / {choice.id} · {choice.enabled === false ? "显式禁用" : choice.version}
          </li>
        ))}
      </ul>
      <p>内置 skills：{value.bundled_skills === false ? "关闭" : "继承或保留"}</p>
      <pre>{value.agents_md || "无独立指令正文"}</pre>
    </details>
  );
}

export function ExtensionsDirectory({ refreshVersion }: { refreshVersion: number }) {
  const { client } = useServices();
  const [version, setVersion] = useState(0);
  const [state, setState] = useState<ExtensionState>();
  const [status, setStatus] = useState<ExtensionStatus>();
  const [history, setHistory] = useState<ExtensionReleasePage>();
  const [release, setRelease] = useState<ExtensionRelease>();
  const [category, setCategory] = useState("");
  const [identity, setIdentity] = useState("");
  const [combination, setCombination] = useState<ExtensionCombination>();
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);

  useEffect(() => {
    const controller = new AbortController();
    void Promise.all([
      client.get<ExtensionState>("/api/v1/extensions/state", controller.signal),
      client.get<ExtensionStatus>("/api/v1/extensions/status?limit=200", controller.signal),
      client.get<ExtensionReleasePage>("/api/v1/extensions/releases", controller.signal),
    ])
      .then(([draft, hosts, releases]) => {
        if (controller.signal.aborted) return;
        setState(draft);
        setStatus(hosts);
        setHistory(releases);
        setError("");
      })
      .catch((err: unknown) => {
        if (!controller.signal.aborted) setError(String(err));
      });
    return () => controller.abort();
  }, [client, refreshVersion, version]);

  async function resolve() {
    const parts = identity.split("/");
    if (parts.length !== 2 || !parts[0] || !parts[1] || !category) {
      setError("选择一个 category，并填写完整的 environment/agent ID。");
      return;
    }
    setLoading(true);
    try {
      const query = new URLSearchParams({
        category,
        environment: parts[0],
        agent: parts[1],
        ...(release ? { release: release.id } : {}),
      });
      setCombination(await client.get<ExtensionCombination>(`/api/v1/extensions/resolve?${query}`));
      setError("");
    } catch (err) {
      setError(String(err));
    } finally {
      setLoading(false);
    }
  }

  const catalog = release?.catalog ?? state?.catalog;
  return (
    <div className={styles.root}>
      <p>资源和指令通过 CLI 管理。草稿、发布、下载、组合就绪与实际采用分别展示。</p>
      <Button variant="ghost" onClick={() => setVersion((v) => v + 1)}>
        重新读取
      </Button>
      {error && <p role="alert">{error}</p>}
      {!catalog ? (
        <EmptyState>正在读取托管扩展…</EmptyState>
      ) : (
        <>
          <Section title="发布与执行端">
            <p>当前发布：{status?.published?.id || "尚未发布"}</p>
            {status?.hosts?.map((host) => (
              <details key={host.host_id}>
                <summary>
                  {host.host_id} · {host.state === "unmigrated" ? "未迁移" : "已启用托管"} ·{" "}
                  {host.online ? "在线观测" : "离线观测"}
                </summary>
                <p>目标：{host.status?.desired_release || "无目标"}</p>
                <p>已下载：{host.status?.downloaded_release || "无下载回执"}</p>
                {host.status?.skill_exclusions && (
                  <p>
                    个人 skill 排除清单：{host.status.skill_exclusions.excluded_paths} 项 ·
                    上次刷新：{host.status.skill_exclusions.refreshed_at || "尚无有效清单"} ·
                    下次启动可刷新：{host.status.skill_exclusions.next_refresh_at || "立即"}
                    {host.status.skill_exclusions.last_error
                      ? ` · ${host.status.skill_exclusions.last_error}`
                      : ""}
                  </p>
                )}
                <ul>
                  {host.status?.prepared?.map((prepared, index) => (
                    <li key={`${prepared.pin.effective_sha256}:${index}`}>
                      {prepared.pin.category_id} / {prepared.pin.agent.local_id} ·{" "}
                      {prepared.ready ? "组合已就绪" : "组合未就绪"} · {prepared.reason}
                    </li>
                  ))}
                </ul>
                <ul>
                  {host.status?.adoptions?.map((adoption) => (
                    <li key={adoption.execution}>
                      {adoption.execution} · {adoption.state} · {adoption.pin.release_id}
                      {adoption.pending_reason ? ` · 待切换：${adoption.pending_reason}` : ""}
                      {adoption.mcp && adoption.mcp.length > 0 && (
                        <details>
                          <summary>MCP 原生观测</summary>
                          <pre>{JSON.stringify(adoption.mcp, null, 2)}</pre>
                        </details>
                      )}
                    </li>
                  ))}
                </ul>
                {host.status?.last_error && <p>{host.status.last_error}</p>}
              </details>
            ))}
            {status?.more && <p>执行端列表有后续页，请通过 CLI status 的 after 游标读取。</p>}
          </Section>
          <Section title="发布历史">
            <Button
              variant="ghost"
              onClick={() => {
                setRelease(undefined);
                setCombination(undefined);
              }}
            >
              查看草稿
            </Button>
            {history?.items?.map((item) => (
              <Button
                key={item.id}
                variant="ghost"
                disabled={loading}
                onClick={() => {
                  void client
                    .get<ExtensionRelease>(
                      `/api/v1/extensions/releases/${encodeURIComponent(item.id)}`,
                    )
                    .then((value) => {
                      setRelease(value);
                      setCombination(undefined);
                    })
                    .catch((err: unknown) => setError(String(err)));
                }}
              >
                {item.id}
              </Button>
            ))}
            {history?.more && (
              <Button
                variant="ghost"
                onClick={() => {
                  void client
                    .get<ExtensionReleasePage>(
                      `/api/v1/extensions/releases?after=${encodeURIComponent(history.next ?? "")}`,
                    )
                    .then((page) =>
                      setHistory({
                        ...page,
                        items: [...(history.items ?? []), ...(page.items ?? [])],
                      }),
                    )
                    .catch((err: unknown) => setError(String(err)));
                }}
              >
                读取更多发布
              </Button>
            )}
            <p>查看版本：{release?.id || `草稿 revision ${state?.revision ?? 0}`}</p>
          </Section>
          <Section title="资源库">
            {(catalog.resources ?? []).length === 0 && <EmptyState>尚未导入资源。</EmptyState>}
            {(catalog.resources ?? []).map((resource) => (
              <details key={`${resource.kind}:${resource.id}:${resource.version}`}>
                <summary>
                  {resource.kind} / {resource.id} @ {resource.version}
                </summary>
                <pre>{JSON.stringify(resource, null, 2)}</pre>
              </details>
            ))}
          </Section>
          <Section title="Category 配置">
            {(catalog.categories ?? []).map((binding) => (
              <Binding key={binding.category_id} value={binding} />
            ))}
          </Section>
          <Section title="Agent 配置">
            {(catalog.agents ?? []).map((binding) => (
              <Binding
                key={`${binding.agent?.execution_environment_id}/${binding.agent?.local_id}`}
                value={binding}
              />
            ))}
          </Section>
          <Section title="有效组合">
            <div className={styles.filters}>
              <Field label="本次 category">
                <Select
                  disabled={loading}
                  value={category}
                  onChange={(e) => {
                    setCategory(e.target.value);
                    setCombination(undefined);
                  }}
                >
                  <option value="">选择一个 category</option>
                  {(catalog.categories ?? []).map((binding) => (
                    <option key={binding.category_id} value={binding.category_id}>
                      {binding.category_id}
                    </option>
                  ))}
                </Select>
              </Field>
              <Field label="完整 Agent ID">
                <Input
                  disabled={loading}
                  value={identity}
                  placeholder="environment/agent"
                  onChange={(e) => {
                    setIdentity(e.target.value);
                    setCombination(undefined);
                  }}
                />
              </Field>
              <Button disabled={loading} onClick={() => void resolve()}>
                查看组合
              </Button>
            </div>
            <p>此处展示托管层。项目扩展及管理员配置仍由执行目录中的 Codex 原生规则决定。</p>
            {combination && (
              <>
                <ul>
                  {(combination.resources ?? []).map((item) => (
                    <li key={`${item.resource.kind}:${item.resource.id}`}>
                      {item.resource.kind} / {item.resource.id} @ {item.resource.version} · 来源{" "}
                      {item.origin}
                    </li>
                  ))}
                </ul>
                <p>内置 skills：{combination.bundled_skills ? "保留" : "关闭"}</p>
                <p>
                  显式禁用：
                  {(combination.disabled ?? [])
                    .map((item) => `${item.kind}/${item.id}`)
                    .join("，") || "无"}
                </p>
                <h3>合成 AGENTS.md</h3>
                <pre>{combination.agents_md || "空正文"}</pre>
                <details>
                  <summary>组合引用</summary>
                  <pre>{JSON.stringify(combination.pin, null, 2)}</pre>
                </details>
              </>
            )}
          </Section>
        </>
      )}
    </div>
  );
}
