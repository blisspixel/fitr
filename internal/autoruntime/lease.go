package autoruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

// installationLease retains deny-write/delete handles on Windows. Ordinary
// installers cannot replace earlier hashed or delayed-load dependencies.
// New directory entries remain a non-atomic same-user boundary, rechecked
// immediately before executable invocation rather than claimed immutable.
type installationLease struct {
	executable, base, root string
	paths                  []string
	files                  []*os.File
	info                   []os.FileInfo
	once                   sync.Once
	err                    error
}

func inspectInstallation(ctx context.Context, executable string) (*installationLease, string, string, error) {
	ctx, cancel := context.WithTimeout(ctx, HashTimeout)
	defer cancel()
	lease, err := lockInstallation(ctx, executable)
	if err != nil {
		return nil, "", "", err
	}
	exeHash, libraries, err := installationDigests(ctx, executable)
	if err == nil {
		err = lease.verify(ctx)
	}
	if err != nil {
		_ = lease.close()
		return nil, "", "", err
	}
	return lease, exeHash, libraries, nil
}

func lockInstallation(ctx context.Context, executable string) (*installationLease, error) {
	if _, err := physicalPath(executable, false); err != nil {
		return nil, err
	}
	base := filepath.Dir(executable)
	root := filepath.Join(base, "lib", "ollama")
	paths, err := installationFiles(ctx, base, root)
	if err != nil {
		return nil, err
	}
	lease := &installationLease{executable: executable, base: base, root: root, paths: paths}
	var total int64
	for _, path := range append([]string{executable}, paths...) {
		if err := lease.open(ctx, path, &total); err != nil {
			_ = lease.close()
			return nil, err
		}
	}
	if err := lease.verify(ctx); err != nil {
		_ = lease.close()
		return nil, err
	}
	return lease, nil
}

func (l *installationLease) open(ctx context.Context, path string, total *int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := physicalPath(path, false); err != nil {
		return err
	}
	f, err := openLockedRead(path)
	if err != nil {
		return err
	}
	l.files = append(l.files, f)
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > MaxFileBytes || info.Size() > MaxRuntimeBytes-*total {
		return errors.New("runtime installation lease exceeds regular-file byte limits")
	}
	_ = os.SameFile(info, info)
	*total += info.Size()
	l.info = append(l.info, info)
	return nil
}

func (l *installationLease) verify(ctx context.Context) error {
	paths, err := installationFiles(ctx, l.base, l.root)
	if err != nil {
		return err
	}
	if !samePaths(paths, l.paths) {
		return errors.New("runtime dependency inventory changed while leased")
	}
	for i, path := range append([]string{l.executable}, l.paths...) {
		if err := ctx.Err(); err != nil {
			return err
		}
		now, err := os.Lstat(path)
		if err != nil {
			return err
		}
		before := l.info[i]
		if !now.Mode().IsRegular() || !os.SameFile(before, now) || before.Size() != now.Size() || !before.ModTime().Equal(now.ModTime()) {
			return errors.New("runtime installation identity changed while leased")
		}
	}
	return nil
}

func (l *installationLease) close() error {
	l.once.Do(func() {
		for _, f := range l.files {
			l.err = errors.Join(l.err, f.Close())
		}
	})
	return l.err
}
