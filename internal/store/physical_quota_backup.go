package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/fileguard"
)

const physicalQuotaBackupDatabaseMaxBytes int64 = 256 << 20

// ReadPhysicalQuotaBindingFromBackup reads the owner-store ledger pin from a
// backup snapshot without opening it as a live Store. The caller supplies the
// held backup root and its expected identity, along with the exact
// database/state.sqlite manifest digest and length. A missing pin is returned
// as (nil, nil); current restore callers must refuse such a snapshot.
//
// The snapshot database and its parent must remain the same single-link,
// regular files/directories throughout inspection. SQLite receives only the
// exact digest-checked bytes read through the held file handle, in a private
// query-only memory database. No pathname SQLite open, owner lock, WAL, SHM,
// generation, or schema write is created. The input and SQLite image copies
// are each bounded by physicalQuotaBackupDatabaseMaxBytes.
func ReadPhysicalQuotaBindingFromBackup(ctx context.Context, backupRoot *os.Root, expectedBackupRootIdentity, expectedDatabaseDigest string, expectedDatabaseBytes int64) (*PhysicalQuotaBinding, error) {
	if backupRoot == nil || !c.ValidDigest(expectedBackupRootIdentity) || !c.ValidDigest(expectedDatabaseDigest) || expectedDatabaseBytes <= 0 || expectedDatabaseBytes > physicalQuotaBackupDatabaseMaxBytes {
		return nil, c.Fail(c.InvalidArgument, "bounded backup database witness required")
	}
	rootPath, err := canonicalInspectionRoot(backupRoot, expectedBackupRootIdentity)
	if err != nil {
		return nil, err
	}
	databaseRoot, err := openInspectionDirectory(backupRoot, rootPath, "database")
	if err != nil {
		return nil, err
	}
	defer databaseRoot.Close()
	databaseRootIdentity, err := fileguard.DirectoryIdentity(databaseRoot)
	if err != nil {
		return nil, err
	}
	if err = verifyInspectionDirectoryPath(filepath.Join(rootPath, "database"), databaseRootIdentity); err != nil {
		return nil, err
	}
	for _, sidecar := range []string{"state.sqlite-wal", "state.sqlite-shm", "state.sqlite-journal"} {
		if _, err = databaseRoot.Lstat(sidecar); err == nil {
			return nil, c.Fail(c.StaleAuthority, "backup snapshot contains an unmanifested SQLite sidecar")
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}

	databaseFile, databaseInfo, err := openInspectionDatabase(databaseRoot, "state.sqlite", expectedDatabaseDigest, expectedDatabaseBytes)
	if err != nil {
		return nil, err
	}
	defer databaseFile.Close()
	if err = verifyInspectionFilePath(databaseRoot, "state.sqlite", databaseInfo); err != nil {
		return nil, err
	}
	image, err := io.ReadAll(io.LimitReader(databaseFile, expectedDatabaseBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(image)) != expectedDatabaseBytes || c.HashBytes(image) != expectedDatabaseDigest {
		return nil, c.Fail(c.StaleAuthority, "backup database image differs from its manifest witness")
	}
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	connection, err := db.Conn(ctx)
	if err != nil {
		return nil, errors.Join(err, db.Close())
	}
	err = connection.Raw(func(driverConnection any) error {
		decoder, ok := driverConnection.(interface{ Deserialize([]byte) error })
		if !ok {
			return c.Fail(c.UnsupportedCapability, "pinned SQLite image decoder unavailable")
		}
		return decoder.Deserialize(image)
	})
	image = nil
	if err == nil {
		_, err = connection.ExecContext(ctx, "PRAGMA query_only=ON")
	}
	if err != nil {
		return nil, errors.Join(err, connection.Close(), db.Close())
	}
	tx, err := connection.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, errors.Join(err, connection.Close(), db.Close())
	}
	binding, inspectErr := physicalQuotaBindingFromQuery(ctx, tx)
	if inspectErr == nil {
		inspectErr = tx.Commit()
	} else {
		inspectErr = errors.Join(inspectErr, tx.Rollback())
	}
	closeErr := errors.Join(connection.Close(), db.Close())
	if err = errors.Join(inspectErr, closeErr); err != nil {
		return nil, err
	}
	if err = verifyInspectionFilePath(databaseRoot, "state.sqlite", databaseInfo); err != nil {
		return nil, err
	}
	if err = verifyInspectionDirectoryPath(filepath.Join(rootPath, "database"), databaseRootIdentity); err != nil {
		return nil, err
	}
	if err = verifyInspectionRootPath(backupRoot, rootPath, expectedBackupRootIdentity); err != nil {
		return nil, err
	}
	if err = hashInspectionFile(databaseFile, expectedDatabaseBytes, expectedDatabaseDigest); err != nil {
		return nil, err
	}
	for _, sidecar := range []string{"state.sqlite-wal", "state.sqlite-shm", "state.sqlite-journal"} {
		if _, err = databaseRoot.Lstat(sidecar); err == nil {
			return nil, c.Fail(c.StaleAuthority, "read-only backup inspection created or observed an SQLite sidecar")
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	return binding, nil
}

func canonicalInspectionRoot(root *os.Root, expectedIdentity string) (string, error) {
	identity, err := fileguard.DirectoryIdentity(root)
	if err != nil {
		return "", err
	}
	if identity != expectedIdentity {
		return "", c.Fail(c.StaleAuthority, "backup root identity changed before physical quota inspection")
	}
	absolute, err := filepath.Abs(root.Name())
	if err != nil {
		return "", err
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil || filepath.Clean(canonical) != filepath.Clean(absolute) {
		return "", c.Fail(c.StaleAuthority, "backup root path is not canonical")
	}
	if err = verifyInspectionRootPath(root, filepath.Clean(absolute), expectedIdentity); err != nil {
		return "", err
	}
	return filepath.Clean(absolute), nil
}

func verifyInspectionRootPath(held *os.Root, path, expectedIdentity string) error {
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil || filepath.Clean(canonical) != filepath.Clean(path) {
		return c.Fail(c.StaleAuthority, "backup root path is missing or redirected")
	}
	heldIdentity, err := fileguard.DirectoryIdentity(held)
	if err != nil {
		return err
	}
	if heldIdentity != expectedIdentity {
		return c.Fail(c.StaleAuthority, "held backup root identity changed")
	}
	current, err := os.OpenRoot(path)
	if err != nil {
		return c.Fail(c.StaleAuthority, "backup root path disappeared or was replaced")
	}
	identity, identityErr := fileguard.DirectoryIdentity(current)
	closeErr := current.Close()
	if err = errors.Join(identityErr, closeErr); err != nil {
		return err
	}
	if identity != expectedIdentity {
		return c.Fail(c.StaleAuthority, "backup root path was replaced")
	}
	return nil
}

func openInspectionDirectory(root *os.Root, rootPath, name string) (*os.Root, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, c.Fail(c.StaleAuthority, "backup database parent is not a real directory")
	}
	directory, err := root.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	identity, identityErr := fileguard.DirectoryIdentity(directory)
	currentInfo, statErr := root.Lstat(name)
	if identityErr == nil && statErr == nil && (!currentInfo.IsDir() || currentInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(info, currentInfo)) {
		statErr = c.Fail(c.StaleAuthority, "backup database parent was replaced")
	}
	if err = errors.Join(identityErr, statErr); err != nil {
		return nil, errors.Join(err, directory.Close())
	}
	if err = verifyInspectionDirectoryPath(filepath.Join(rootPath, name), identity); err != nil {
		return nil, errors.Join(err, directory.Close())
	}
	return directory, nil
}

func verifyInspectionDirectoryPath(path, expectedIdentity string) error {
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil || filepath.Clean(canonical) != filepath.Clean(path) {
		return c.Fail(c.StaleAuthority, "backup database directory is missing or redirected")
	}
	current, err := os.OpenRoot(path)
	if err != nil {
		return c.Fail(c.StaleAuthority, "backup database directory disappeared or was replaced")
	}
	identity, identityErr := fileguard.DirectoryIdentity(current)
	closeErr := current.Close()
	if err = errors.Join(identityErr, closeErr); err != nil {
		return err
	}
	if identity != expectedIdentity {
		return c.Fail(c.StaleAuthority, "backup database directory was replaced")
	}
	return nil
}

func openInspectionDatabase(root *os.Root, name, expectedDigest string, expectedBytes int64) (*os.File, os.FileInfo, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() != expectedBytes {
		return nil, nil, c.Fail(c.StaleAuthority, "backup database file differs from its manifest witness")
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, nil, err
	}
	opened, err := file.Stat()
	if err == nil && (!opened.Mode().IsRegular() || !os.SameFile(info, opened) || opened.Size() != expectedBytes) {
		err = c.Fail(c.StaleAuthority, "backup database changed while opening")
	}
	if err == nil {
		err = fileguard.SingleLink(file)
	}
	if err == nil {
		err = hashInspectionFile(file, expectedBytes, expectedDigest)
	}
	if err == nil {
		_, err = file.Seek(0, io.SeekStart)
	}
	if err != nil {
		return nil, nil, errors.Join(err, file.Close())
	}
	return file, info, nil
}

func verifyInspectionFilePath(root *os.Root, name string, expected os.FileInfo) error {
	info, err := root.Lstat(name)
	if err != nil {
		return c.Fail(c.StaleAuthority, "backup database path disappeared")
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || !os.SameFile(expected, info) {
		return c.Fail(c.StaleAuthority, "backup database path was replaced or linked")
	}
	file, err := root.Open(name)
	if err != nil {
		return err
	}
	opened, err := file.Stat()
	if err == nil && (!opened.Mode().IsRegular() || !os.SameFile(expected, opened)) {
		err = c.Fail(c.StaleAuthority, "backup database changed while inspecting")
	}
	if err == nil {
		err = fileguard.SingleLink(file)
	}
	return errors.Join(err, file.Close())
}

func hashInspectionFile(file *os.File, expectedBytes int64, expectedDigest string) error {
	if file == nil || expectedBytes < 0 || expectedBytes > physicalQuotaBackupDatabaseMaxBytes || !c.ValidDigest(expectedDigest) {
		return c.Fail(c.InvalidArgument, "bounded backup database hash witness required")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	hash := sha256.New()
	read, err := io.Copy(hash, io.LimitReader(file, expectedBytes+1))
	if err != nil {
		return err
	}
	if read != expectedBytes || hex.EncodeToString(hash.Sum(nil)) != expectedDigest {
		return c.Fail(c.StaleAuthority, "backup database bytes differ from their manifest witness")
	}
	return nil
}
