package fbhttp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/filebrowser/filebrowser/v2/files"
	"github.com/spf13/afero"
)

// Overwrites keep their partial content beside the original until completion.
// The metadata lives in the upload cache so another replica can resume it.
type stagedUpload struct {
	Path        string      `json:"path"`
	Destination string      `json:"destination"`
	Original    string      `json:"original"`
	Stamp       string      `json:"stamp"`
	Mode        os.FileMode `json:"mode"`
}

func (s *stagedUpload) save(cache UploadCache, key, stamp string) error {
	if s == nil {
		return cache.SetStamp(key, stamp)
	}
	s.Stamp = stamp
	value, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return cache.SetStamp(key, string(value))
}

func uploadFile(cache UploadCache, key, target string, d *data) (*files.FileInfo, *stagedUpload, error) {
	stamp, err := cache.GetStamp(key)
	if err != nil {
		return nil, nil, err
	}
	var staged *stagedUpload
	name := target
	if strings.HasPrefix(stamp, "{") {
		staged = &stagedUpload{}
		if err := json.Unmarshal([]byte(stamp), staged); err != nil {
			return nil, nil, err
		}
		if staged.Path == "" || filepath.Dir(staged.Path) != filepath.Dir(staged.Destination) ||
			!strings.HasPrefix(filepath.Base(staged.Path), ".filebrowser-upload-") {
			return nil, nil, fmt.Errorf("invalid staged upload path %q for destination %q", staged.Path, staged.Destination)
		}
		name, stamp = staged.Path, staged.Stamp
	}
	file, err := uploadFileInfo(d.user.Fs, name)
	if err != nil {
		return nil, nil, err
	}
	if stamp == "" || stamp != uploadStamp(file) {
		return nil, nil, fmt.Errorf("upload target changed; restart the upload")
	}
	return file, staged, nil
}

// Callers authorize the destination before inspecting internal upload files.
func uploadFileInfo(fsys afero.Fs, name string) (*files.FileInfo, error) {
	info, err := fsys.Stat(name)
	if err != nil {
		return nil, err
	}
	return &files.FileInfo{Fs: fsys, Path: name, Size: info.Size(), ModTime: info.ModTime(), IsDir: info.IsDir()}, nil
}

func (s *stagedUpload) commit(d *data, target string) error {
	if s == nil {
		return nil
	}
	if !d.user.Perm.Modify || !d.Check(target) {
		return os.ErrPermission
	}
	resolved, err := files.ResolvedPath(d.user.Fs, target)
	if err != nil {
		return err
	}
	if resolved != s.Destination {
		return fmt.Errorf("upload destination changed; restart the upload")
	}
	info, err := d.user.Fs.Stat(resolved)
	if lstater, ok := d.user.Fs.(afero.Lstater); ok {
		info, _, err = lstater.LstatIfPossible(resolved)
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || fmt.Sprintf("%d:%d", info.ModTime().UnixNano(), info.Size()) != s.Original {
		return fmt.Errorf("upload destination changed; restart the upload")
	}
	if err := d.user.Fs.Chmod(s.Path, s.Mode); err != nil {
		return err
	}
	return d.user.Fs.Rename(s.Path, resolved)
}
