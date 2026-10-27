import type { ArtifactFile } from "../api/generated/api.ts";

export type PreviewFile = ArtifactFile & {
  media_type?: string;
  source_task_id?: string;
  source_message_id?: string;
};
export function artifactFiles(value: unknown): PreviewFile[] {
  return Array.isArray(value)
    ? value.filter(
        (file): file is PreviewFile =>
          !!file &&
          typeof file === "object" &&
          typeof file.file_id === "string" &&
          typeof file.name === "string" &&
          typeof file.size === "number" &&
          typeof file.sha256 === "string",
      )
    : [];
}
export function isImage(file: PreviewFile): boolean {
  return file.media_type
    ? /^image\/(png|jpeg|gif|webp)$/.test(file.media_type)
    : /\.(png|jpe?g|gif|webp)$/i.test(file.name);
}
/** Resolve only exact immutable identities or unique manifest-relative names. */
export function resolveFile(
  source: string,
  files: readonly PreviewFile[],
): PreviewFile | undefined {
  const id = files.find((file) => file.file_id === source);
  if (id) return id;
  if (/^(?:[a-z][a-z\d+.-]*:|[/\\])|(?:^|\/)\.\.(?:\/|$)/i.test(source)) return undefined;
  let name: string;
  try {
    name = decodeURIComponent(source.replace(/^\.\//, ""));
  } catch {
    return undefined;
  }
  const matches = files.filter((file) => file.name === name);
  return matches.length === 1 ? matches[0] : undefined;
}
export function sourceFileIds(sources: readonly string[]): string[] {
  return [
    ...new Set(sources.flatMap((source) => source.match(/\bfile_[0-9A-HJKMNP-TV-Z]{26}\b/g) ?? [])),
  ];
}
