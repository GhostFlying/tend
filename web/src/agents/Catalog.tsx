import { Button, Input, Select, Textarea, EmptyState } from "../ui/index.ts";
import { runtimeLabel } from "./runtimeLabel.ts";
import { putCategoryCommand } from "./actions.ts";
import { useEffect, useMemo, useState } from "react";
import type {
  AgentCategory,
  AgentInventoryProjection,
  AgentInventoryDefinition,
  AgentExecutor,
  RunnerAgentCatalog,
  PutAgentCategoryPayload,
} from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { useStore } from "../state/store.ts";
import { useDurableOp } from "../app/useDurableOp.ts";
import { WorkItemRead } from "../workitems/live.ts";
import type { OpOutcome } from "../app/ops.ts";
import styles from "./Catalog.module.css";

export function AgentCatalogView() {
  const { live } = useServices();
  const categories = useStore(live.agents.categories.state);
  const hosted = useStore(live.agents.hosted.state);
  const snapshot = useStore(live.snapshot);
  const [edit, setEdit] = useState<AgentCategory | "new" | null>(null);
  useEffect(() => {
    void live.agents.openCategories("all");
    void live.agents.openHosted();
    return () => live.agents.categories.close();
  }, [live]);
  if (edit)
    return (
      <CategoryEditor
        key={edit === "new" ? "new" : edit.category_id}
        category={edit === "new" ? undefined : edit}
        onBack={() => setEdit(null)}
      />
    );
  return (
    <section className={styles.root}>
      <p className={styles.subtitle}>管理全局类别，查看各环境Agent登记。</p>
      <div className={styles.categories}>
        {(categories.value?.items ?? []).map((category) => (
          <Button key={category.category_id} onClick={() => setEdit(category)}>
            {category.title} · {category.category_id}
            {category.state !== "active" ? "（已停用）" : ""}
          </Button>
        ))}
        <Button onClick={() => setEdit("new")}>新增类别</Button>
        {categories.value?.more && (
          <Button onClick={() => void live.agents.openCategories("all", categories.value?.next)}>
            下一页类别
          </Button>
        )}
      </div>
      {(categories.error || hosted.error) && (
        <p role="status" className={styles.warning}>
          部分登记暂时无法读取，显示上次结果。
          <Button onClick={() => live.agents.refresh()}>重新读取</Button>
        </p>
      )}
      <div className={styles.table}>
        <div className={styles.head}>
          <span>Agent</span>
          <span>类别</span>
          <span>运行时</span>
          <span>模型与effort</span>
          <span>登记</span>
          <span>最近上报</span>
          <span>主机</span>
        </div>
        <div className={styles.group}>托管环境</div>
        {hosted.value?.state === "unconfigured" ? (
          <EmptyState>尚未配置托管Agent</EmptyState>
        ) : hosted.value ? (
          <AgentRows projection={hosted.value.projection} />
        ) : (
          <p className={styles.muted}>正在读取托管登记…</p>
        )}
        {(snapshot?.hosts ?? []).map((host) => (
          <RunnerGroup key={host.host_id} id={host.host_id} />
        ))}
        {snapshot && snapshot.hosts?.length === 0 && <EmptyState>暂无已登记执行主机。</EmptyState>}
      </div>
      <p className={styles.muted}>
        定义保存在对应环境，这里展示已登记摘要。类别用于选择，不改变任务角色权限。
      </p>
    </section>
  );
}
function RunnerGroup({ id }: { id: string }) {
  const { client, live } = useServices();
  const [expanded, setExpanded] = useState(true);
  const reader = useMemo(() => new WorkItemRead<RunnerAgentCatalog>(client), [client]);
  const state = useStore(reader.state);
  useEffect(() => {
    if (!expanded) return;
    void reader.open(`/api/v1/hosts/${encodeURIComponent(id)}/agents`);
    const stop = live.snapshot.subscribe(() => void reader.refresh());
    return () => {
      stop();
      reader.close();
    };
  }, [expanded, id, reader, live]);
  return (
    <>
      <Button
        variant="ghost"
        className={styles.group}
        aria-expanded={expanded}
        onClick={() => setExpanded(!expanded)}
      >
        {id}
      </Button>
      {expanded &&
        (state.value?.projection ? (
          <AgentRows
            projection={state.value.projection}
            executors={state.value.executors}
            online={state.value.online}
            at={state.value.last_heartbeat_at_ms}
          />
        ) : (
          <p role="status" className={styles.muted}>
            {state.error
              ? "暂时无法读取登记。"
              : state.value
                ? "此主机尚未登记Agent。"
                : "正在读取…"}
          </p>
        ))}
    </>
  );
}
function AgentRows({
  projection,
  executors,
  online,
  at,
}: {
  projection: AgentInventoryProjection;
  executors?: AgentExecutor[] | undefined;
  online?: boolean | undefined;
  at?: number | undefined;
}) {
  const [selected, setSelected] = useState("");
  return (
    <>
      {(projection.agents ?? []).map((agent) => {
        const runtime = projection.runtimes?.find(
          (r) => r.runtime_id === agent.runtime_binding_ref.runtime_id,
        );
        const observed = executors?.find(
          (e) =>
            e.pin.identity.execution_environment_id === agent.identity.execution_environment_id &&
            e.pin.identity.local_id === agent.identity.local_id,
        );
        const key = `${agent.identity.execution_environment_id}/${agent.identity.local_id}`;
        return (
          <div key={key}>
            <Button
              variant="ghost"
              className={styles.row}
              aria-expanded={selected === key}
              onClick={() => setSelected(selected === key ? "" : key)}
            >
              <span>{agent.display_name}</span>
              <span>{agent.categories?.join("、")}</span>
              <span>{runtimeLabel(runtime)}</span>
              <span>
                {agent.requested_model || "未指定"} {agent.requested_effort}
              </span>
              <span>
                {["staged", "registered_staged"].includes(agent.registration)
                  ? "已声明"
                  : agent.registration}
              </span>
              <span>
                {observed ? (observed.available ? "最近上报可用" : "最近上报不可用") : "未观测"}
              </span>
              <span>{online === undefined ? "—" : online ? "在线" : "离线"}</span>
            </Button>
            {selected === key && <AgentDetails agent={agent} observed={observed} at={at} />}
          </div>
        );
      })}
    </>
  );
}
function AgentDetails({
  agent,
  observed,
  at,
}: {
  agent: AgentInventoryDefinition;
  observed: AgentExecutor | undefined;
  at: number | undefined;
}) {
  return (
    <div className={styles.details}>
      <p>显示名：{agent.display_name}</p>
      <p>本地ID：{agent.identity.local_id}</p>
      <p>环境：{agent.identity.execution_environment_id}</p>
      <p>类别：{agent.categories?.join("、")}</p>
      <p>用途：{agent.purposes?.join("、")}</p>
      <p>配置：{agent.enabled ? "启用" : "未启用"}</p>
      <p>定义版本：{agent.revision}</p>
      {observed && (
        <>
          <p>观测版本：{observed.pin.inventory_revision}</p>
          <p>上报时间：{at ? new Date(at).toLocaleString("zh-CN") : "未记录"}</p>
          <p>最近上报：{observed.available ? "可用" : observed.reason || "不可用"}</p>
        </>
      )}
    </div>
  );
}
function CategoryEditor({
  category,
  onBack,
}: {
  category?: AgentCategory | undefined;
  onBack(): void;
}) {
  const { client, live } = useServices();
  const [id, setID] = useState(category?.category_id ?? "");
  const [title, setTitle] = useState(category?.title ?? "");
  const [description, setDescription] = useState(category?.description ?? "");
  const [state, setState] = useState(category?.state ?? "active");
  const operation = useDurableOp(client, `category:${category?.category_id ?? "new"}`);
  const [result, setResult] = useState<OpOutcome | null>(null);
  const original = operation.original;
  const payload = original?.payload as PutAgentCategoryPayload | undefined;
  const save = async (reconcile = false) => {
    try {
      const out = await operation.execute(
        reconcile
          ? undefined
          : putCategoryCommand(id, category?.revision ?? 0, { title, description, state }),
      );
      setResult(out);
      if (out.kind === "done") live.agents.refresh();
    } catch {
      setResult({ kind: "unknown", message: "保存结果待核实，请核实原请求。" });
    }
  };
  return (
    <section className={`${styles.root} ${styles.form}`}>
      <h2>{category ? "编辑类别" : "新增类别"}</h2>
      <label>
        类别ID
        <Input
          value={original?.target.id ?? id}
          disabled={!!category || operation.busy || operation.pending || result?.kind === "done"}
          onChange={(e) => setID(e.target.value)}
        />
      </label>
      <label>
        名称
        <Input
          value={payload?.title ?? title}
          disabled={operation.busy || operation.pending || result?.kind === "done"}
          onChange={(e) => setTitle(e.target.value)}
        />
      </label>
      <label>
        类别说明
        <Textarea
          value={payload?.description ?? description}
          disabled={operation.busy || operation.pending || result?.kind === "done"}
          onChange={(e) => setDescription(e.target.value)}
        />
      </label>
      <label>
        状态
        <Select
          value={payload?.state ?? state}
          disabled={operation.busy || operation.pending || result?.kind === "done"}
          onChange={(e) => setState(e.target.value)}
        >
          <option value="active">启用</option>
          <option value="retired">停用</option>
        </Select>
      </label>
      <p className={styles.muted}>类别用于选择，不改变任务角色权限。</p>
      {operation.storageError && <p role="alert">{operation.storageError}</p>}
      {result && <p role="status">{result.kind === "done" ? "类别已保存。" : result.message}</p>}
      <div className={styles.actions}>
        {operation.pending ? (
          <Button
            className={styles.primary}
            disabled={operation.busy}
            onClick={() => void save(true)}
          >
            核实原保存请求
          </Button>
        ) : (
          <Button
            className={styles.primary}
            disabled={operation.busy || result?.kind === "done" || !id.trim() || !title.trim()}
            onClick={() => void save()}
          >
            保存类别
          </Button>
        )}
        <Button className={styles.button} onClick={onBack}>
          返回Agent目录
        </Button>
      </div>
    </section>
  );
}
