// Fixed intervals are durations; wall-clock appointments require an explicit zone.
export function localTime(epoch: number, zone: string): string {
  const p = new Intl.DateTimeFormat("en-CA", {
    timeZone: zone,
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    hourCycle: "h23",
  }).formatToParts(epoch);
  const get = (type: string) => p.find((x) => x.type === type)?.value ?? "";
  return `${get("year")}-${get("month")}-${get("day")}T${get("hour")}:${get("minute")}`;
}
export function utcTime(value: string, zone: string): number {
  if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}$/.test(value)) throw new Error("请选择完整日期和时间");
  if (!zone || zone === "Local") throw new Error("请填写明确的 IANA 时区");
  const naive = Date.parse(`${value}Z`);
  if (!Number.isFinite(naive)) throw new Error("日期无效");
  const candidates = new Set<number>();
  // Sample offsets around the date to cover both sides of DST transitions.
  for (const hours of [-48, -24, -12, 0, 12, 24, 48]) {
    const sample = naive + hours * 3600000;
    const offset = Date.parse(`${localTime(sample, zone)}Z`) - sample;
    const candidate = naive - offset;
    if (localTime(candidate, zone) === value) candidates.add(candidate);
  }
  if (candidates.size === 0)
    throw new Error("该时区没有这个时间（可能处于夏令时跳转），请选择其他时间");
  if (candidates.size > 1) throw new Error("该时间在夏令时切换中重复，请切换 UTC 并指定准确时刻");
  return [...candidates][0]!;
}
export function followupDate(epoch: number | undefined, zone = "UTC"): string {
  if (!epoch) return "—";
  try {
    return `${localTime(epoch, zone).replace("T", " ")} ${zone}`;
  } catch {
    return new Date(epoch).toISOString();
  }
}
