package safeoutput

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mansiverma897993/noz-in/internal/atomicfile"
)

// PinnedDirectory owns identity-verified handles to a real directory. Root
// confines filesystem operations to that opened directory even if its lexical
// path is subsequently replaced.
type PinnedDirectory struct {
	path string
	root *os.Root
	file *os.File
}

// OpenDirectory opens and pins an existing real directory. The final path must
// not be a symbolic link, and the opened identity is checked against the path
// before and after it is pinned.
func OpenDirectory(path string) (*PinnedDirectory, error) {
	return openDirectory(path, false, 0)
}

// OpenOrCreateDirectory pins the deepest existing real ancestor, creates each
// missing component relative to that handle, and pins the result. A missing
// component that appears concurrently, or an existing non-directory or final
// symbolic link, fails closed.
func OpenOrCreateDirectory(path string, perm os.FileMode) (*PinnedDirectory, error) {
	if perm&^os.FileMode(0o777) != 0 {
		return nil, fmt.Errorf("create output directory %q: unsupported permissions %v", path, perm)
	}
	return openDirectory(path, true, perm)
}

// Path returns the cleaned absolute path presented by the caller.
func (directory *PinnedDirectory) Path() string {
	if directory == nil {
		return ""
	}
	return directory.path
}

// Root returns the handle-relative filesystem root for the pinned directory.
// The root remains owned by directory and must not be closed by the caller.
func (directory *PinnedDirectory) Root() *os.Root {
	if directory == nil {
		return nil
	}
	return directory.root
}

// File returns the opened directory handle used for durability barriers. The
// file remains owned by directory and must not be closed by the caller.
func (directory *PinnedDirectory) File() *os.File {
	if directory == nil {
		return nil
	}
	return directory.file
}

// Close releases both handles owned by the pinned directory.
func (directory *PinnedDirectory) Close() error {
	if directory == nil {
		return nil
	}
	return errors.Join(directory.root.Close(), directory.file.Close())
}

func openDirectory(path string, create bool, perm os.FileMode) (*PinnedDirectory, error) {
	return openDirectoryWithSync(path, create, perm, syncDirectoryRoot)
}

func openDirectoryWithSync(
	path string,
	create bool,
	perm os.FileMode,
	syncParent func(*os.Root) error,
) (*PinnedDirectory, error) {
	absolute, ancestor, components, before, err := existingDirectoryAncestor(path)
	if err != nil {
		return nil, err
	}
	if !create && len(components) > 0 {
		return nil, fmt.Errorf("open output directory %q: %w", absolute, os.ErrNotExist)
	}
	current, err := pinExistingDirectory(ancestor, before)
	if err != nil {
		return nil, err
	}
	closeCurrent := true
	defer func() {
		if closeCurrent {
			_ = current.Close()
		}
	}()
	for _, component := range components {
		next, err := createAndPinDirectoryComponent(current, component, absolute, perm, syncParent)
		if err != nil {
			return nil, err
		}
		if err := current.Close(); err != nil {
			_ = next.Close()
			return nil, fmt.Errorf("close parent while opening output directory %q: %w", absolute, err)
		}
		current = next
	}

	file, err := openVerifiedDirectoryFile(current, absolute)
	if err != nil {
		return nil, err
	}
	closeCurrent = false
	return &PinnedDirectory{path: absolute, root: current, file: file}, nil
}

func pinExistingDirectory(path string, before os.FileInfo) (*os.Root, error) {
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, fmt.Errorf("pin existing output directory ancestor %q: %w", path, err)
	}
	opened, openedErr := root.Stat(".")
	after, afterErr := os.Lstat(path)
	if openedErr == nil && afterErr == nil && after.IsDir() && opened.IsDir() &&
		os.SameFile(before, after) && os.SameFile(before, opened) {
		return root, nil
	}
	_ = root.Close()
	if openedErr != nil {
		return nil, fmt.Errorf("inspect pinned output directory ancestor %q: %w", path, openedErr)
	}
	if afterErr != nil {
		return nil, fmt.Errorf("reinspect output directory ancestor %q: %w", path, afterErr)
	}
	return nil, fmt.Errorf("output directory ancestor %q changed while it was pinned", path)
}

func createAndPinDirectoryComponent(
	parent *os.Root,
	component string,
	absolute string,
	perm os.FileMode,
	syncParent func(*os.Root) error,
) (*os.Root, error) {
	if err := parent.Mkdir(component, perm); err != nil {
		return nil, fmt.Errorf("create output directory component %q in %q: %w", component, absolute, err)
	}
	if err := syncParent(parent); err != nil {
		return nil, fmt.Errorf("persist output directory component %q in %q: %w", component, absolute, err)
	}
	before, err := parent.Lstat(component)
	if err != nil {
		return nil, fmt.Errorf("inspect output directory component %q in %q: %w", component, absolute, err)
	}
	if !before.IsDir() {
		return nil, fmt.Errorf("output directory %q is not a real directory: component %q is not a directory", absolute, component)
	}
	next, err := parent.OpenRoot(component)
	if err != nil {
		return nil, fmt.Errorf("open output directory component %q in %q: %w", component, absolute, err)
	}
	after, afterErr := parent.Lstat(component)
	opened, openedErr := next.Stat(".")
	if afterErr == nil && openedErr == nil && after.IsDir() && opened.IsDir() &&
		os.SameFile(before, after) && os.SameFile(before, opened) {
		return next, nil
	}
	_ = next.Close()
	if afterErr != nil {
		return nil, fmt.Errorf("reinspect output directory component %q in %q: %w", component, absolute, afterErr)
	}
	if openedErr != nil {
		return nil, fmt.Errorf("inspect opened output directory component %q in %q: %w", component, absolute, openedErr)
	}
	return nil, fmt.Errorf("output directory %q changed while component %q was opened", absolute, component)
}

func openVerifiedDirectoryFile(root *os.Root, path string) (*os.File, error) {
	file, err := root.Open(".")
	if err != nil {
		return nil, fmt.Errorf("open pinned output directory handle %q: %w", path, err)
	}
	rootInfo, rootErr := root.Stat(".")
	fileInfo, fileErr := file.Stat()
	if rootErr == nil && fileErr == nil && rootInfo.IsDir() && fileInfo.IsDir() && os.SameFile(rootInfo, fileInfo) {
		return file, nil
	}
	_ = file.Close()
	if rootErr != nil {
		return nil, fmt.Errorf("inspect pinned output directory %q: %w", path, rootErr)
	}
	if fileErr != nil {
		return nil, fmt.Errorf("inspect pinned output directory handle %q: %w", path, fileErr)
	}
	return nil, fmt.Errorf("output directory %q changed while its handles were pinned", path)
}

func syncDirectoryRoot(root *os.Root) error {
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	syncErr := atomicfile.SyncOpenedDirectory(directory)
	closeErr := directory.Close()
	return errors.Join(syncErr, closeErr)
}

func existingDirectoryAncestor(path string) (string, string, []string, os.FileInfo, error) {
	if path == "" {
		return "", "", nil, nil, fmt.Errorf("output directory is empty")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", "", nil, nil, fmt.Errorf("resolve output directory %q: %w", path, err)
	}
	absolute = filepath.Clean(absolute)
	ancestor := absolute
	components := make([]string, 0, 4)
	for {
		info, inspectErr := os.Lstat(ancestor)
		if inspectErr == nil {
			if !info.IsDir() {
				return "", "", nil, nil, fmt.Errorf("output directory %q is not a real directory", ancestor)
			}
			return absolute, ancestor, components, info, nil
		}
		if !errors.Is(inspectErr, os.ErrNotExist) {
			return "", "", nil, nil, fmt.Errorf("inspect output directory %q: %w", ancestor, inspectErr)
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return "", "", nil, nil, fmt.Errorf("output directory %q has no existing ancestor", absolute)
		}
		components = append([]string{filepath.Base(ancestor)}, components...)
		ancestor = parent
	}
}
