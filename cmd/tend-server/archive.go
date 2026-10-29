package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/GhostFlying/tend/internal/archive"
	"github.com/GhostFlying/tend/internal/archiveseed"
	"github.com/GhostFlying/tend/internal/protocol"
)

type archiveCommandDependencies struct {
	now    func() time.Time
	ids    protocol.IDGen
	random io.Reader
}

func defaultArchiveDependencies() archiveCommandDependencies {
	now := time.Now
	return archiveCommandDependencies{now: now, ids: protocol.NewULIDGen(now, nil), random: rand.Reader}
}

// runOfflineArchive is dispatched before config/secret/store/network setup.
// Only seed initializes a fresh destination; all historical reads stay in archive.
func runOfflineArchive(ctx context.Context, args []string, output, errorOutput io.Writer, deps archiveCommandDependencies) error {
	if len(args) == 0 {
		return errors.New("archive expects capture|verify|list|show|original|resources|resource|export-seed|seed")
	}
	action := args[0]
	flags := flag.NewFlagSet("archive "+action, flag.ContinueOnError)
	flags.SetOutput(errorOutput)
	var root, database, blobs, destination, resources, kind, id, after, selectionPath, bindingsPath, sourceBuild string
	var stopped bool
	limit, index := 100, 0
	if action == "capture" {
		flags.StringVar(&database, "database", "", "stopped source SQLite ledger")
		flags.StringVar(&blobs, "blob-root", "", "source original-file directory; defaults to database.files")
		flags.StringVar(&destination, "output", "", "new private archive directory")
		flags.StringVar(&resources, "resources", "", "explicit owner-selected resource JSON list; never auto-collects home/config")
		flags.StringVar(&sourceBuild, "source-build", "", "known source build, omitted when unknown")
		flags.BoolVar(&stopped, "writers-stopped", false, "operator confirms all source writers are stopped; does not stop or reconcile them")
	} else {
		flags.StringVar(&root, "archive", "", "sealed archive directory")
		switch action {
		case "verify", "resources":
		case "list":
			flags.StringVar(&kind, "kind", "", "fixed historical object kind")
			flags.StringVar(&after, "after", "", "last object ID")
			flags.IntVar(&limit, "limit", 100, "page size 1..200")
		case "show", "original":
			flags.StringVar(&kind, "kind", "", "historical object/original kind")
			flags.StringVar(&id, "id", "", "opaque historical ID")
		case "resource":
			flags.StringVar(&id, "id", "", "resource ID from resources index")
			flags.IntVar(&index, "index", 0, "selected payload index")
		case "export-seed", "seed":
			flags.StringVar(&selectionPath, "selection", "", "explicit static-selection JSON")
			if action == "seed" {
				flags.StringVar(&bindingsPath, "bindings", "", "fresh node-binding JSON for exactly the selected hosts")
				flags.StringVar(&destination, "target", "", "exclusively new ledger directory; never starts the server")
			}
		default:
			return fmt.Errorf("unknown archive action %q", action)
		}
	}
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("archive commands take named flags only")
	}
	encode := func(value any) error {
		encoder := json.NewEncoder(output)
		encoder.SetIndent("", "  ")
		return encoder.Encode(value)
	}
	if action == "capture" {
		if database == "" || destination == "" {
			return errors.New("archive capture requires --database and --output")
		}
		var selected []archive.ResourceSource
		if resources != "" {
			if err := archive.ReadJSON(resources, &selected); err != nil {
				return err
			}
		}
		manifest, err := archive.Capture(ctx, archive.CaptureOptions{Database: database, BlobRoot: blobs, Destination: destination, WritersStopped: stopped, CapturedAt: deps.now(), SourceBuild: sourceBuild, Resources: selected})
		if err != nil {
			return err
		}
		return encode(manifest)
	}
	if root == "" {
		return errors.New("archive directory is required")
	}
	if action == "verify" {
		manifest, err := archive.Verify(ctx, root)
		if err != nil {
			return err
		}
		return encode(manifest)
	}
	reader, err := archive.Open(ctx, root)
	if err != nil {
		return err
	}
	defer reader.Close()
	switch action {
	case "list":
		records, err := reader.Records(ctx, kind, after, limit)
		if err != nil {
			return err
		}
		return encode(records)
	case "show":
		if id == "" {
			return errors.New("show requires an opaque ID")
		}
		record, err := reader.Record(ctx, kind, id)
		if err != nil {
			return err
		}
		return encode(record)
	case "original":
		if id == "" {
			return errors.New("original requires an opaque ID")
		}
		_, file, err := reader.OpenOriginal(kind, id)
		if err != nil {
			return err
		}
		defer file.Close()
		return copyArchiveOutput(ctx, output, file)
	case "resources":
		return encode(reader.Manifest().Resources)
	case "resource":
		file, err := reader.OpenResource(id, index)
		if err != nil {
			return err
		}
		defer file.Close()
		return copyArchiveOutput(ctx, output, file)
	case "export-seed", "seed":
		if selectionPath == "" {
			return errors.New("static seed requires an explicit --selection")
		}
		var selection archive.Selection
		if err := archive.ReadJSON(selectionPath, &selection); err != nil {
			return err
		}
		seed, err := reader.ExportSeed(ctx, selection)
		if err != nil {
			return err
		}
		if action == "export-seed" {
			return encode(seed)
		}
		if destination == "" {
			return errors.New("seed requires a new --target")
		}
		var bindings archiveseed.Bindings
		if bindingsPath != "" {
			if err := archive.ReadJSON(bindingsPath, &bindings); err != nil {
				return err
			}
		}
		receipt, err := archiveseed.Initialize(ctx, archiveseed.Options{Target: destination, ArchiveRoot: root, Seed: seed, Bindings: bindings, Now: deps.now, IDs: deps.ids, Random: deps.random})
		if err != nil {
			return err
		}
		return encode(receipt)
	}
	return errors.New("unsupported archive command")
}

func copyArchiveOutput(ctx context.Context, output io.Writer, input io.Reader) error {
	buffer := make([]byte, 64<<10)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, readErr := input.Read(buffer)
		if n > 0 {
			written, err := output.Write(buffer[:n])
			if err != nil {
				return err
			}
			if written != n {
				return io.ErrShortWrite
			}
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}
