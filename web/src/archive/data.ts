import type {
  ArchiveCatalog,
  ArchiveOriginal,
  ArchiveOriginalPage,
  ArchiveRecord,
  ArchiveRecordsPage,
  ArchiveResourcePage,
  ArchiveSummary,
  DownloadURLResponse,
} from "../api/generated/api.ts";
import type { Reader } from "../live/data.ts";
import { WorkItemRead } from "../workitems/live.ts";

type OriginalKind = "attachment" | "image" | "deliverable";
const archivePath = (id: string): string => `/api/v1/archives/${encodeURIComponent(id)}`;
const pageQuery = (after?: string): string => (after ? `?${new URLSearchParams({ after })}` : "");

/** Sealed archive reads never reuse historical IDs against current endpoints. */
export class Archives {
  readonly catalog: WorkItemRead<ArchiveCatalog>;
  readonly detail: WorkItemRead<ArchiveSummary>;
  readonly records: WorkItemRead<ArchiveRecordsPage>;
  readonly record: WorkItemRead<ArchiveRecord>;
  readonly originals: WorkItemRead<ArchiveOriginalPage>;
  readonly original: WorkItemRead<ArchiveOriginal>;
  readonly resources: WorkItemRead<ArchiveResourcePage>;

  constructor(private readonly api: Reader) {
    this.catalog = new WorkItemRead(api);
    this.detail = new WorkItemRead(api);
    this.records = new WorkItemRead(api);
    this.record = new WorkItemRead(api);
    this.originals = new WorkItemRead(api);
    this.original = new WorkItemRead(api);
    this.resources = new WorkItemRead(api);
  }

  openCatalog(): Promise<void> {
    return this.catalog.open("/api/v1/archives");
  }
  open(id: string): Promise<void> {
    return this.detail.open(archivePath(id));
  }
  openRecords(id: string, kind: string, after?: string): Promise<void> {
    return this.records.open(
      `${archivePath(id)}/records/${encodeURIComponent(kind)}${pageQuery(after)}`,
    );
  }
  openRecord(id: string, kind: string, recordID: string): Promise<void> {
    return this.record.open(
      `${archivePath(id)}/records/${encodeURIComponent(kind)}/${encodeURIComponent(recordID)}`,
    );
  }
  openOriginals(id: string, kind: OriginalKind, after?: string): Promise<void> {
    return this.originals.open(`${archivePath(id)}/originals/${kind}${pageQuery(after)}`);
  }
  openOriginal(id: string, kind: OriginalKind, originalID: string): Promise<void> {
    return this.original.open(
      `${archivePath(id)}/originals/${kind}/${encodeURIComponent(originalID)}/metadata`,
    );
  }
  openResources(id: string, after?: string): Promise<void> {
    return this.resources.open(`${archivePath(id)}/resources${pageQuery(after)}`);
  }
  originalDownload(
    id: string,
    kind: OriginalKind,
    originalID: string,
    signal?: AbortSignal,
  ): Promise<DownloadURLResponse> {
    return this.api.get(
      `${archivePath(id)}/originals/${kind}/${encodeURIComponent(originalID)}/download-url`,
      signal,
    );
  }
  resourceDownload(
    id: string,
    resourceID: string,
    index: number,
    signal?: AbortSignal,
  ): Promise<DownloadURLResponse> {
    return this.api.get(
      `${archivePath(id)}/resources/${encodeURIComponent(resourceID)}/files/${index}/download-url`,
      signal,
    );
  }
  private reads() {
    return [
      this.catalog,
      this.detail,
      this.records,
      this.record,
      this.originals,
      this.original,
      this.resources,
    ];
  }
  refresh(): void {
    for (const read of this.reads()) void read.refresh();
  }
  stop(): void {
    for (const read of this.reads()) read.close();
  }
}
