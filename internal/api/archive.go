package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"mime"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/GhostFlying/tend/internal/archive"
	"github.com/GhostFlying/tend/internal/protocol"
)

// ConfiguredArchive is a handle supplied by local server assembly. The creator
// owns Close; handlers never open an archive or initialize historical services.
type ConfiguredArchive struct {
	Reader *archive.Reader
	State  protocol.ArchiveState
	Reason string
}
type archiveHandle struct {
	reader    *archive.Reader
	summary   protocol.ArchiveSummary
	originals []protocol.ArchiveOriginal
	resources []protocol.ArchiveResource
}

func archiveHandles(configured map[string]ConfiguredArchive) map[string]archiveHandle {
	handles := map[string]archiveHandle{}
	for id, entry := range configured {
		h := archiveHandle{reader: entry.Reader, summary: protocol.ArchiveSummary{ArchiveID: id, State: entry.State, Reason: entry.Reason}, originals: []protocol.ArchiveOriginal{}, resources: []protocol.ArchiveResource{}}
		if entry.Reader == nil {
			if h.summary.State != protocol.ArchiveUnsupported {
				h.summary.State = protocol.ArchiveUnavailable
			}
		} else {
			manifest := entry.Reader.Manifest()
			h.summary.State = protocol.ArchiveReady
			h.summary.Reason = ""
			h.summary.Metadata = &protocol.ArchiveMetadata{FormatVersion: manifest.FormatVersion, DeploymentID: manifest.DeploymentID, Schema: manifest.Schema, CreatedAt: manifest.CreatedAt, SourceBuild: manifest.SourceBuild, RecordKinds: entry.Reader.SupportedKinds(), OriginalCount: len(manifest.Originals), ResourceCount: len(manifest.Resources)}
			for _, original := range manifest.Originals {
				h.originals = append(h.originals, protocol.ArchiveOriginal{Kind: original.Kind, ID: original.ID, Name: original.Name, MediaType: original.MediaType, Size: original.Size, SHA256: original.SHA256})
			}
			slices.SortFunc(h.originals, func(a, b protocol.ArchiveOriginal) int {
				if c := strings.Compare(a.Kind, b.Kind); c != 0 {
					return c
				}
				return strings.Compare(a.ID, b.ID)
			})
			files := map[string]archive.Payload{}
			for _, file := range manifest.Files {
				files[file.Path] = file
			}
			for _, resource := range manifest.Resources {
				item := protocol.ArchiveResource{ID: resource.ID, Kind: resource.Kind, HostID: resource.HostID, RunID: resource.RunID, ThreadID: resource.ThreadID, ParentThreadID: resource.ParentThreadID, Scope: resource.Scope, Status: resource.Status, Reason: resource.Reason, JournalSchema: resource.JournalSchema, Files: []protocol.ArchiveResourceFile{}}
				for i, relative := range resource.Files {
					file := files[relative]
					item.Files = append(item.Files, protocol.ArchiveResourceFile{Index: i, Name: path.Base(relative), Size: file.Size, SHA256: file.SHA256})
				}
				h.resources = append(h.resources, item)
			}
			slices.SortFunc(h.resources, func(a, b protocol.ArchiveResource) int { return strings.Compare(a.ID, b.ID) })
		}
		handles[id] = h
	}
	return handles
}
func (s *Server) archiveCatalog(w http.ResponseWriter, r *http.Request) {
	if _, err := s.owner(r); err != nil {
		s.writeError(w, r, err)
		return
	}
	page := protocol.ArchiveCatalog{Items: []protocol.ArchiveSummary{}}
	for _, id := range slices.Sorted(maps.Keys(s.archives)) {
		page.Items = append(page.Items, s.archives[id].summary)
	}
	writeJSON(w, http.StatusOK, page)
}
func (s *Server) readyArchive(w http.ResponseWriter, r *http.Request) (archiveHandle, bool) {
	h, ok := s.archives[r.PathValue("archive")]
	if !ok {
		s.writeError(w, r, protocol.Errorf(protocol.CodeNotFound, "archive is not configured"))
		return h, false
	}
	if h.reader == nil {
		writeJSON(w, http.StatusServiceUnavailable, protocol.APIResponse{Error: protocol.Errorf(protocol.CodeInvalidTransition, "configured archive is %s", h.summary.State)})
		return h, false
	}
	return h, true
}
func (s *Server) ownerArchive(w http.ResponseWriter, r *http.Request) (archiveHandle, bool) {
	if _, err := s.owner(r); err != nil {
		s.writeError(w, r, err)
		return archiveHandle{}, false
	}
	return s.readyArchive(w, r)
}
func (s *Server) archiveDetail(w http.ResponseWriter, r *http.Request) {
	h, ok := s.ownerArchive(w, r)
	if ok {
		writeJSON(w, http.StatusOK, h.summary)
	}
}
func archivePageParameters(r *http.Request) (string, int, error) {
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 200 {
			return "", 0, protocol.Errorf(protocol.CodeInvalidRequest, "limit must be 1..200")
		}
		limit = n
	}
	return r.URL.Query().Get("after"), limit, nil
}
func (s *Server) archiveRecords(w http.ResponseWriter, r *http.Request) {
	h, ok := s.ownerArchive(w, r)
	if !ok {
		return
	}
	kind := r.PathValue("kind")
	if !slices.Contains(h.summary.Metadata.RecordKinds, kind) {
		s.writeError(w, r, protocol.Errorf(protocol.CodeInvalidRequest, "kind is not supported by this archive"))
		return
	}
	after, limit, err := archivePageParameters(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	items, err := h.reader.Records(r.Context(), kind, after, limit)
	if err != nil {
		s.archiveReadError(w, r, err)
		return
	}
	page := protocol.ArchiveRecordsPage{ArchiveID: h.summary.ArchiveID, Schema: h.summary.Metadata.Schema, Kind: kind, Items: items}
	if len(items) == limit {
		page.NextAfter, _ = h.reader.RecordID(kind, items[len(items)-1])
	}
	writeJSON(w, http.StatusOK, page)
}
func (s *Server) archiveRecord(w http.ResponseWriter, r *http.Request) {
	h, ok := s.ownerArchive(w, r)
	if !ok {
		return
	}
	kind := r.PathValue("kind")
	if !slices.Contains(h.summary.Metadata.RecordKinds, kind) {
		s.writeError(w, r, protocol.Errorf(protocol.CodeInvalidRequest, "kind is not supported by this archive"))
		return
	}
	item, err := h.reader.Record(r.Context(), kind, r.PathValue("id"))
	if err != nil {
		s.archiveReadError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, protocol.ArchiveRecord{ArchiveID: h.summary.ArchiveID, Schema: h.summary.Metadata.Schema, Kind: kind, ID: r.PathValue("id"), Data: item})
}
func originalKind(kind string) bool {
	return kind == "attachment" || kind == "image" || kind == "deliverable"
}
func (s *Server) archiveOriginals(w http.ResponseWriter, r *http.Request) {
	h, ok := s.ownerArchive(w, r)
	if !ok {
		return
	}
	kind := r.PathValue("kind")
	if !originalKind(kind) {
		s.writeError(w, r, protocol.Errorf(protocol.CodeInvalidRequest, "unsupported original kind"))
		return
	}
	after, limit, err := archivePageParameters(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	page := protocol.ArchiveOriginalPage{ArchiveID: h.summary.ArchiveID, Items: []protocol.ArchiveOriginal{}}
	for _, item := range h.originals {
		if item.Kind != kind || item.ID <= after {
			continue
		}
		if len(page.Items) == limit {
			page.NextAfter = page.Items[len(page.Items)-1].ID
			break
		}
		page.Items = append(page.Items, item)
	}
	writeJSON(w, http.StatusOK, page)
}
func (h archiveHandle) original(kind, id string) (protocol.ArchiveOriginal, error) {
	if !originalKind(kind) {
		return protocol.ArchiveOriginal{}, protocol.Errorf(protocol.CodeInvalidRequest, "unsupported original kind")
	}
	for _, item := range h.originals {
		if item.Kind == kind && item.ID == id {
			return item, nil
		}
	}
	return protocol.ArchiveOriginal{}, sql.ErrNoRows
}
func (s *Server) archiveOriginalMetadata(w http.ResponseWriter, r *http.Request) {
	h, ok := s.ownerArchive(w, r)
	if !ok {
		return
	}
	item, err := h.original(r.PathValue("kind"), r.PathValue("id"))
	if err != nil {
		s.archiveReadError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

// Scope uses the existing signed Kind field; components cannot alias each other.
func archiveGrantKind(r *http.Request, resource bool) string {
	kind := []string{"archive-original", r.PathValue("archive"), r.PathValue("kind")}
	if resource {
		kind = []string{"archive-resource", r.PathValue("archive"), r.PathValue("index")}
	}
	encoded, _ := json.Marshal(kind)
	return string(encoded)
}
func (s *Server) archiveOriginalURL(w http.ResponseWriter, r *http.Request) {
	p, err := s.owner(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	h, ok := s.readyArchive(w, r)
	if !ok {
		return
	}
	if _, err = h.original(r.PathValue("kind"), r.PathValue("id")); err != nil {
		s.archiveReadError(w, r, err)
		return
	}
	s.archiveDownloadURL(w, r, p, archiveGrantKind(r, false), r.URL.Query().Get("inline") == "1")
}
func (s *Server) archiveDownloadURL(w http.ResponseWriter, r *http.Request, p protocol.Principal, kind string, inline bool) {
	query := url.Values{"download": {s.signFileGrant(fileGrant{Login: p.Subject, ID: r.PathValue("id"), Kind: kind, Inline: inline, Issued: s.cfg.Now().UnixMilli()})}}
	if inline {
		query.Set("inline", "1")
	}
	route := strings.TrimSuffix(r.URL.EscapedPath(), "/download-url")
	writeJSON(w, http.StatusOK, protocol.DownloadURLResponse{URL: route + "?" + query.Encode()})
}
func (s *Server) archiveOriginal(w http.ResponseWriter, r *http.Request) {
	if _, err := s.filePrincipal(r, archiveGrantKind(r, false)); err != nil {
		s.writeError(w, r, err)
		return
	}
	h, ok := s.readyArchive(w, r)
	if !ok {
		return
	}
	item, err := h.original(r.PathValue("kind"), r.PathValue("id"))
	if err != nil {
		s.archiveReadError(w, r, err)
		return
	}
	_, file, err := h.reader.OpenOriginal(item.Kind, item.ID)
	if err != nil {
		s.archiveReadError(w, r, err)
		return
	}
	defer file.Close()
	s.serveArchiveFile(w, r, item.Name, item.MediaType, item.SHA256, file, item.Kind == "deliverable", false)
}
func (s *Server) archiveResources(w http.ResponseWriter, r *http.Request) {
	h, ok := s.ownerArchive(w, r)
	if !ok {
		return
	}
	after, limit, err := archivePageParameters(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	page := protocol.ArchiveResourcePage{ArchiveID: h.summary.ArchiveID, Items: []protocol.ArchiveResource{}}
	for _, item := range h.resources {
		if item.ID <= after {
			continue
		}
		if len(page.Items) == limit {
			page.NextAfter = page.Items[len(page.Items)-1].ID
			break
		}
		page.Items = append(page.Items, item)
	}
	writeJSON(w, http.StatusOK, page)
}
func (h archiveHandle) resourceFile(r *http.Request) (protocol.ArchiveResourceFile, error) {
	index, err := strconv.Atoi(r.PathValue("index"))
	if err != nil || index < 0 {
		return protocol.ArchiveResourceFile{}, protocol.Errorf(protocol.CodeInvalidRequest, "resource index must be nonnegative")
	}
	for _, item := range h.resources {
		if item.ID == r.PathValue("id") {
			if index < len(item.Files) {
				return item.Files[index], nil
			}
			break
		}
	}
	return protocol.ArchiveResourceFile{}, sql.ErrNoRows
}
func (s *Server) archiveResourceURL(w http.ResponseWriter, r *http.Request) {
	p, err := s.owner(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	h, ok := s.readyArchive(w, r)
	if !ok {
		return
	}
	if r.URL.Query().Get("inline") == "1" {
		s.writeError(w, r, protocol.Errorf(protocol.CodeInvalidRequest, "resources download as attachments"))
		return
	}
	if _, err = h.resourceFile(r); err != nil {
		s.archiveReadError(w, r, err)
		return
	}
	s.archiveDownloadURL(w, r, p, archiveGrantKind(r, true), false)
}
func (s *Server) archiveResourceFile(w http.ResponseWriter, r *http.Request) {
	if _, err := s.filePrincipal(r, archiveGrantKind(r, true)); err != nil {
		s.writeError(w, r, err)
		return
	}
	h, ok := s.readyArchive(w, r)
	if !ok {
		return
	}
	item, err := h.resourceFile(r)
	if err != nil {
		s.archiveReadError(w, r, err)
		return
	}
	file, err := h.reader.OpenResource(r.PathValue("id"), item.Index)
	if err != nil {
		s.archiveReadError(w, r, err)
		return
	}
	defer file.Close()
	s.serveArchiveFile(w, r, item.Name, "", item.SHA256, file, false, true)
}
func (s *Server) archiveReadError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		s.writeError(w, r, protocol.Errorf(protocol.CodeNotFound, "archived object not found"))
		return
	}
	if pe, ok := errors.AsType[*protocol.Error](err); ok {
		s.writeError(w, r, pe)
		return
	}
	s.log.ErrorContext(r.Context(), "historical archive read failed", "archive_id", r.PathValue("archive"), "err", err)
	writeJSON(w, http.StatusServiceUnavailable, protocol.APIResponse{Error: protocol.Errorf(protocol.CodeInvalidTransition, "archived content is unavailable")})
}
func (s *Server) serveArchiveFile(w http.ResponseWriter, r *http.Request, name, declared, hash string, body io.Reader, deliverable, resource bool) {
	stream, ok := body.(io.ReadSeeker)
	if !ok {
		s.archiveReadError(w, r, errors.New("archive stream is not seekable"))
		return
	}
	media, disposition := "application/octet-stream", "attachment"
	if !resource && r.URL.Query().Get("inline") == "1" {
		detected := declared
		if deliverable {
			var header [512]byte
			n, err := stream.Read(header[:])
			if err != nil && err != io.EOF {
				s.archiveReadError(w, r, err)
				return
			}
			if _, err = stream.Seek(0, io.SeekStart); err != nil {
				s.archiveReadError(w, r, err)
				return
			}
			detected = http.DetectContentType(header[:n])
		}
		switch detected {
		case "image/png", "image/jpeg", "image/gif", "image/webp":
			media, disposition = detected, "inline"
		default:
			if deliverable {
				ext := strings.ToLower(path.Ext(name))
				if ext == ".html" || ext == ".htm" {
					media, disposition = "text/html; charset=utf-8", "inline"
				}
			}
		}
	}
	w.Header().Set("Content-Type", media)
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": name}))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-SHA256", hash)
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'; script-src 'none'; style-src 'unsafe-inline'; img-src data: blob:; font-src data:; connect-src 'none'; form-action 'none'; base-uri 'none'; frame-ancestors 'self'")
	http.ServeContent(w, r, name, time.Time{}, stream)
}
