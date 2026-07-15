// Package backup creates and verifies versioned MailWisp backup bundles.
package backup

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"mailwisp/internal/contentstore"
)

const (
	formatName       = "mailwisp-backup"
	formatVersion    = 1
	manifestFileName = "manifest.json"
	databaseFileName = "database.dump"
	contentFileName  = "content.tar.gz"
	maxManifestBytes = 1 << 20
)

// DatabaseMetadata records the exact PostgreSQL toolchain used for a dump.
type DatabaseMetadata struct {
	ServerVersion    string
	DumpVersion      string
	RestoreVersion   string
	MigrationVersion int64
}

// DatabaseDumper streams a PostgreSQL custom-format dump.
type DatabaseDumper interface {
	Dump(context.Context, io.Writer) (DatabaseMetadata, error)
}

// ContentArchiver streams canonical immutable objects.
type ContentArchiver interface {
	WriteArchive(context.Context, io.Writer) (contentstore.ArchiveStats, error)
}

// Manifest is the strict V1 backup bundle contract.
type Manifest struct {
	Format     string             `json:"format"`
	Version    int                `json:"version"`
	CreatedAt  time.Time          `json:"created_at"`
	PostgreSQL PostgreSQLManifest `json:"postgresql"`
	Database   ComponentManifest  `json:"database"`
	Content    ContentManifest    `json:"content"`
}

// PostgreSQLManifest identifies server, tools, and schema migration state.
type PostgreSQLManifest struct {
	ServerVersion    string `json:"server_version"`
	DumpVersion      string `json:"pg_dump_version"`
	RestoreVersion   string `json:"pg_restore_version"`
	MigrationVersion int64  `json:"migration_version"`
}

// ComponentManifest identifies one fixed bundle component.
type ComponentManifest struct {
	File      string `json:"file"`
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"size_bytes"`
}

// ContentManifest extends component integrity with logical object counts.
type ContentManifest struct {
	ComponentManifest
	Objects           int64 `json:"objects"`
	UncompressedBytes int64 `json:"uncompressed_bytes"`
}

// VerifiedBundle contains validated component paths and its strict manifest.
type VerifiedBundle struct {
	Root         string
	Manifest     Manifest
	DatabasePath string
	ContentPath  string
}

// Create writes a complete bundle to a sibling partial directory and publishes
// it with one rename. The destination must not already exist.
func Create(
	ctx context.Context,
	destination string,
	createdAt time.Time,
	database DatabaseDumper,
	content ContentArchiver,
) (Manifest, error) {
	if database == nil {
		return Manifest{}, errors.New("database dumper is required")
	}
	if content == nil {
		return Manifest{}, errors.New("content archiver is required")
	}
	if strings.TrimSpace(destination) == "" {
		return Manifest{}, errors.New("backup destination is required")
	}
	if createdAt.IsZero() {
		return Manifest{}, errors.New("backup creation time is required")
	}
	absoluteDestination, err := filepath.Abs(destination)
	if err != nil {
		return Manifest{}, fmt.Errorf("resolve backup destination: %w", err)
	}
	if _, err := os.Lstat(absoluteDestination); err == nil {
		return Manifest{}, errors.New("backup destination already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return Manifest{}, fmt.Errorf("inspect backup destination: %w", err)
	}
	parent := filepath.Dir(absoluteDestination)
	parentInfo, err := os.Stat(parent)
	if err != nil {
		return Manifest{}, fmt.Errorf("inspect backup parent directory: %w", err)
	}
	if !parentInfo.IsDir() {
		return Manifest{}, errors.New("backup parent path is not a directory")
	}
	partial, err := partialPath(absoluteDestination)
	if err != nil {
		return Manifest{}, err
	}
	if err := os.Mkdir(partial, 0o700); err != nil {
		return Manifest{}, fmt.Errorf("create partial backup directory: %w", err)
	}
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(partial)
		}
	}()
	partialRoot, err := os.OpenRoot(partial)
	if err != nil {
		return Manifest{}, fmt.Errorf("open partial backup directory: %w", err)
	}

	databaseComponent, metadata, err := writeDatabase(ctx, partialRoot, database)
	if err != nil {
		_ = partialRoot.Close()
		return Manifest{}, err
	}
	contentComponent, stats, err := writeContent(ctx, partialRoot, content)
	if err != nil {
		_ = partialRoot.Close()
		return Manifest{}, err
	}
	manifest := Manifest{
		Format:    formatName,
		Version:   formatVersion,
		CreatedAt: createdAt.UTC(),
		PostgreSQL: PostgreSQLManifest{
			ServerVersion:    metadata.ServerVersion,
			DumpVersion:      metadata.DumpVersion,
			RestoreVersion:   metadata.RestoreVersion,
			MigrationVersion: metadata.MigrationVersion,
		},
		Database: databaseComponent,
		Content: ContentManifest{
			ComponentManifest: contentComponent,
			Objects:           stats.Objects,
			UncompressedBytes: stats.Bytes,
		},
	}
	writeManifestErr := writeManifest(partialRoot, manifest)
	closeRootErr := partialRoot.Close()
	if writeManifestErr != nil || closeRootErr != nil {
		return Manifest{}, errors.Join(writeManifestErr, closeRootErr)
	}
	if err := syncDirectory(partial); err != nil {
		return Manifest{}, fmt.Errorf("sync partial backup directory: %w", err)
	}
	if err := os.Rename(partial, absoluteDestination); err != nil {
		return Manifest{}, fmt.Errorf("publish backup bundle: %w", err)
	}
	if err := syncDirectory(parent); err != nil {
		_ = os.Rename(absoluteDestination, partial)
		_ = syncDirectory(parent)
		return Manifest{}, fmt.Errorf("sync backup parent directory: %w", err)
	}
	published = true
	return manifest, nil
}

// Verify validates bundle layout, strict manifest syntax, sizes, and SHA-256
// before a restore is allowed to mutate its target.
func Verify(ctx context.Context, bundleRoot string) (VerifiedBundle, error) {
	root, verified, err := openVerifiedBundle(ctx, bundleRoot)
	if err != nil {
		return VerifiedBundle{}, err
	}
	if err := root.Close(); err != nil {
		return VerifiedBundle{}, fmt.Errorf("close verified backup bundle: %w", err)
	}
	return verified, nil
}

func openVerifiedBundle(ctx context.Context, bundleRoot string) (*os.Root, VerifiedBundle, error) {
	absoluteRoot, err := filepath.Abs(bundleRoot)
	if err != nil {
		return nil, VerifiedBundle{}, fmt.Errorf("resolve backup bundle: %w", err)
	}
	rootInfo, err := os.Lstat(absoluteRoot)
	if err != nil {
		return nil, VerifiedBundle{}, fmt.Errorf("inspect backup bundle: %w", err)
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return nil, VerifiedBundle{}, errors.New("backup bundle root must be a real directory")
	}
	root, err := os.OpenRoot(absoluteRoot)
	if err != nil {
		return nil, VerifiedBundle{}, fmt.Errorf("open backup bundle root: %w", err)
	}
	fail := func(err error) (*os.Root, VerifiedBundle, error) {
		return nil, VerifiedBundle{}, errors.Join(err, root.Close())
	}
	openedInfo, err := root.Stat(".")
	if err != nil {
		return fail(fmt.Errorf("inspect opened backup bundle: %w", err))
	}
	if !openedInfo.IsDir() || !os.SameFile(rootInfo, openedInfo) {
		return fail(errors.New("backup bundle root changed while opening"))
	}
	directory, err := root.Open(".")
	if err != nil {
		return fail(fmt.Errorf("open backup bundle directory: %w", err))
	}
	entries, readEntriesErr := directory.ReadDir(-1)
	closeDirectoryErr := directory.Close()
	if readEntriesErr != nil || closeDirectoryErr != nil {
		return fail(errors.Join(readEntriesErr, closeDirectoryErr))
	}
	if len(entries) != 3 {
		return fail(fmt.Errorf("backup bundle contains %d entries, want 3", len(entries)))
	}
	allowed := map[string]struct{}{manifestFileName: {}, databaseFileName: {}, contentFileName: {}}
	for _, entry := range entries {
		if _, ok := allowed[entry.Name()]; !ok || entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return fail(fmt.Errorf("unexpected backup bundle entry %q", entry.Name()))
		}
	}
	manifest, err := readManifest(root)
	if err != nil {
		return fail(err)
	}
	if err := validateManifest(manifest); err != nil {
		return fail(err)
	}
	if err := verifyComponent(ctx, root, manifest.Database); err != nil {
		return fail(fmt.Errorf("verify database component: %w", err))
	}
	if err := verifyComponent(ctx, root, manifest.Content.ComponentManifest); err != nil {
		return fail(fmt.Errorf("verify content component: %w", err))
	}
	return root, VerifiedBundle{
		Root:         absoluteRoot,
		Manifest:     manifest,
		DatabasePath: filepath.Join(absoluteRoot, databaseFileName),
		ContentPath:  filepath.Join(absoluteRoot, contentFileName),
	}, nil
}

func writeDatabase(ctx context.Context, root *os.Root, database DatabaseDumper) (ComponentManifest, DatabaseMetadata, error) {
	file, err := root.OpenFile(databaseFileName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return ComponentManifest{}, DatabaseMetadata{}, fmt.Errorf("create database dump: %w", err)
	}
	hashed := &hashingWriter{writer: file, hash: sha256.New()}
	metadata, dumpErr := database.Dump(ctx, hashed)
	syncErr := file.Sync()
	closeErr := file.Close()
	if dumpErr != nil || syncErr != nil || closeErr != nil {
		return ComponentManifest{}, DatabaseMetadata{}, errors.Join(dumpErr, syncErr, closeErr)
	}
	return ComponentManifest{File: databaseFileName, SHA256: hex.EncodeToString(hashed.hash.Sum(nil)), SizeBytes: hashed.bytes}, metadata, nil
}

func writeContent(ctx context.Context, root *os.Root, content ContentArchiver) (ComponentManifest, contentstore.ArchiveStats, error) {
	file, err := root.OpenFile(contentFileName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return ComponentManifest{}, contentstore.ArchiveStats{}, fmt.Errorf("create content archive: %w", err)
	}
	hashed := &hashingWriter{writer: file, hash: sha256.New()}
	stats, archiveErr := content.WriteArchive(ctx, hashed)
	syncErr := file.Sync()
	closeErr := file.Close()
	if archiveErr != nil || syncErr != nil || closeErr != nil {
		return ComponentManifest{}, stats, errors.Join(archiveErr, syncErr, closeErr)
	}
	return ComponentManifest{File: contentFileName, SHA256: hex.EncodeToString(hashed.hash.Sum(nil)), SizeBytes: hashed.bytes}, stats, nil
}

func writeManifest(root *os.Root, manifest Manifest) error {
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("encode backup manifest: %w", err)
	}
	encoded = append(encoded, '\n')
	file, err := root.OpenFile(manifestFileName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create backup manifest: %w", err)
	}
	_, writeErr := file.Write(encoded)
	syncErr := file.Sync()
	closeErr := file.Close()
	return errors.Join(writeErr, syncErr, closeErr)
}

func readManifest(root *os.Root) (Manifest, error) {
	info, err := root.Stat(manifestFileName)
	if err != nil {
		return Manifest{}, fmt.Errorf("inspect backup manifest: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > maxManifestBytes {
		return Manifest{}, errors.New("backup manifest is not a bounded regular file")
	}
	file, err := root.Open(manifestFileName)
	if err != nil {
		return Manifest{}, fmt.Errorf("open backup manifest: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, maxManifestBytes+1))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode backup manifest: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Manifest{}, errors.New("backup manifest contains trailing data")
	}
	return manifest, nil
}

func validateManifest(manifest Manifest) error {
	if manifest.Format != formatName || manifest.Version != formatVersion {
		return errors.New("unsupported backup manifest format or version")
	}
	_, createdOffset := manifest.CreatedAt.Zone()
	if manifest.CreatedAt.IsZero() || createdOffset != 0 {
		return errors.New("backup manifest creation time must be UTC")
	}
	if strings.TrimSpace(manifest.PostgreSQL.ServerVersion) == "" || strings.TrimSpace(manifest.PostgreSQL.DumpVersion) == "" || strings.TrimSpace(manifest.PostgreSQL.RestoreVersion) == "" || manifest.PostgreSQL.MigrationVersion < 0 {
		return errors.New("backup manifest PostgreSQL metadata is invalid")
	}
	if err := validateComponent(manifest.Database, databaseFileName); err != nil {
		return err
	}
	if err := validateComponent(manifest.Content.ComponentManifest, contentFileName); err != nil {
		return err
	}
	if manifest.Content.Objects < 0 || manifest.Content.UncompressedBytes < 0 {
		return errors.New("backup manifest content statistics are invalid")
	}
	return nil
}

func validateComponent(component ComponentManifest, expectedFile string) error {
	if component.File != expectedFile || component.SizeBytes < 0 || len(component.SHA256) != sha256.Size*2 || strings.ToLower(component.SHA256) != component.SHA256 {
		return fmt.Errorf("backup component %q metadata is invalid", expectedFile)
	}
	decoded, err := hex.DecodeString(component.SHA256)
	if err != nil || len(decoded) != sha256.Size {
		return fmt.Errorf("backup component %q digest is invalid", expectedFile)
	}
	return nil
}

func verifyComponent(ctx context.Context, root *os.Root, component ComponentManifest) error {
	file, err := openVerifiedComponent(ctx, root, component)
	if err != nil {
		return err
	}
	return file.Close()
}

func openVerifiedComponent(ctx context.Context, root *os.Root, component ComponentManifest) (*os.File, error) {
	info, err := root.Lstat(component.File)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("backup component is not a regular file")
	}
	file, err := root.Open(component.File)
	if err != nil {
		return nil, err
	}
	openedInfo, err := file.Stat()
	if err != nil {
		return nil, errors.Join(err, file.Close())
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		return nil, errors.Join(errors.New("backup component changed while opening"), file.Close())
	}
	hash := sha256.New()
	read, err := io.CopyBuffer(hash, &contextReader{ctx: ctx, source: file}, make([]byte, 32*1024))
	if err != nil {
		return nil, errors.Join(err, file.Close())
	}
	if read != component.SizeBytes || hex.EncodeToString(hash.Sum(nil)) != component.SHA256 {
		return nil, errors.Join(errors.New("backup component size or digest mismatch"), file.Close())
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, errors.Join(fmt.Errorf("rewind verified backup component: %w", err), file.Close())
	}
	return file, nil
}

func partialPath(destination string) (string, error) {
	random := make([]byte, 8)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("generate partial backup name: %w", err)
	}
	return filepath.Join(filepath.Dir(destination), "."+filepath.Base(destination)+".partial-"+hex.EncodeToString(random)), nil
}

type hashingWriter struct {
	writer io.Writer
	hash   hash.Hash
	bytes  int64
}

func (w *hashingWriter) Write(data []byte) (int, error) {
	written, err := w.writer.Write(data)
	if written > 0 {
		if _, hashErr := w.hash.Write(data[:written]); hashErr != nil {
			return written, errors.Join(err, hashErr)
		}
		w.bytes += int64(written)
	}
	return written, err
}

type contextReader struct {
	ctx    context.Context
	source io.Reader
}

func (r *contextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.source.Read(buffer)
}
