export const number = new Intl.NumberFormat("zh-CN");
export function field(value: number, known: number, requests: number): string {
  if (!known) return "未提供";
  return `${number.format(value)}${known < requests ? `（部分，${number.format(known)}/${number.format(requests)} 个请求）` : ""}`;
}
