package fbhttp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/filebrowser/filebrowser/v2/files"
	"github.com/spf13/afero"
)

// maxPatchDrainBytes bounds how much of a rejected PATCH body is discarded to
// keep the connection reusable. It comfortably covers a default-sized chunk;
// beyond that the body is not worth reading just to throw away.
const maxPatchDrainBytes = 32 << 20 // 32MB

func uploadKey(d *data, path string) string {
	return strconv.FormatUint(uint64(d.user.ID), 10) + ":" + d.user.FullPath(path)
}

func uploadStamp(file *files.FileInfo) string {
	return fmt.Sprintf("%d:%d", file.ModTime.UnixNano(), file.Size)
}

func validateUploadFile(cache UploadCache, key string, file *files.FileInfo) error {
	stamp, err := cache.GetStamp(key)
	if err != nil {
		return err
	}
	if stamp == "" || stamp != uploadStamp(file) {
		return fmt.Errorf("upload target changed; restart the upload")
	}
	return nil
}

func withUploadLock(cache UploadCache, fn handleFunc) handleFunc {
	return func(w http.ResponseWriter, r *http.Request, d *data) (int, error) {
		resolved, err := files.ResolvedPath(d.user.Fs, r.URL.Path)
		if err != nil {
			return errToStatus(err), err
		}
		unlock, err := cache.Lock(d.user.FullPath(resolved))
		if err != nil {
			return http.StatusConflict, err
		}
		defer unlock()
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
		defer cancel()
		r = r.WithContext(ctx)
		if body, ok := r.Body.(*deadlineBody); ok {
			body.deadline, _ = ctx.Deadline()
		}
		r.Body = &uploadBody{ReadCloser: r.Body, ctx: ctx}
		_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(30 * time.Second))
		return fn(w, r, d)
	}
}

type uploadBody struct {
	io.ReadCloser
	ctx context.Context
}

func (b *uploadBody) Read(p []byte) (int, error) {
	if err := b.ctx.Err(); err != nil {
		return 0, err
	}
	if b.ReadCloser == nil {
		return 0, io.EOF
	}
	return b.ReadCloser.Read(p)
}

// drainRequestBody discards what the client already put on the wire for a
// request the handler answered without reading. net/http only drains 256KiB on
// its own before giving up and closing the connection, and a connection closed
// while the client is still streaming a chunk reaches the browser as a transport
// error instead of the status we replied with. The client then cannot tell a
// conflict from a network fault, retries blindly, and the upload stalls.
func drainRequestBody(r *http.Request) {
	if r.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, maxPatchDrainBytes))
}

// keepUploadActive periodically touches the cache entry to prevent eviction during transfer
func keepUploadActive(cache UploadCache, filePath string) func() {
	stop := make(chan bool)

	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				cache.Touch(filePath)
			}
		}
	}()

	return func() {
		close(stop)
	}
}

func tusPostHandler(cache UploadCache) handleFunc {
	return withUser(withUploadLock(cache, func(w http.ResponseWriter, r *http.Request, d *data) (int, error) {
		if !d.user.Perm.Create || !d.Check(r.URL.Path) {
			return http.StatusForbidden, nil
		}
		uploadLength, err := getUploadLength(r)
		if err != nil || uploadLength < 0 || uploadLength == int64(^uint64(0)>>1) {
			return http.StatusBadRequest, fmt.Errorf("invalid upload length")
		}

		file, err := files.NewFileInfo(&files.FileOptions{
			Fs:         d.user.Fs,
			Path:       r.URL.Path,
			Modify:     d.user.Perm.Modify,
			Expand:     false,
			ReadHeader: d.server.TypeDetectionByHeader,
			Checker:    d,
		})
		switch {
		case errors.Is(err, afero.ErrFileNotFound):
			dirPath := filepath.Dir(r.URL.Path)
			if _, statErr := d.user.Fs.Stat(dirPath); os.IsNotExist(statErr) {
				if mkdirErr := d.user.Fs.MkdirAll(dirPath, d.settings.DirMode); mkdirErr != nil {
					return http.StatusInternalServerError, err
				}
			}
		case err != nil:
			return errToStatus(err), err
		}

		fileFlags := os.O_CREATE | os.O_WRONLY | os.O_EXCL

		// if file exists
		if file != nil {
			if file.IsDir {
				return http.StatusBadRequest, fmt.Errorf("cannot upload to a directory %s", file.RealPath())
			}

			// Existing files will remain untouched unless explicitly instructed to override
			if r.URL.Query().Get("override") != "true" {
				return http.StatusConflict, nil
			}

			// Permission for overwriting the file
			if !d.user.Perm.Modify {
				return http.StatusForbidden, nil
			}

			fileFlags = os.O_WRONLY | os.O_TRUNC
		}
		if err := d.RunBeforeHook("upload", r.URL.Path, "", d.user); err != nil {
			return http.StatusInternalServerError, err
		}
		if err := r.Context().Err(); err != nil {
			return http.StatusRequestTimeout, err
		}
		// Confirm the cache accepts the upload before touching existing content.
		key := uploadKey(d, r.URL.Path)
		if err := cache.Register(key, uploadLength, nil); err != nil {
			return http.StatusServiceUnavailable, err
		}

		openFile, err := d.user.Fs.OpenFile(r.URL.Path, fileFlags, d.settings.FileMode)
		if err != nil {
			_ = cache.Complete(key)
			return errToStatus(err), err
		}
		defer openFile.Close()

		file, err = files.NewFileInfo(&files.FileOptions{
			Fs:         d.user.Fs,
			Path:       r.URL.Path,
			Modify:     d.user.Perm.Modify,
			Expand:     false,
			ReadHeader: false,
			Checker:    d,
			Content:    false,
		})
		if err != nil {
			return errToStatus(err), err
		}

		// Keep abandoned partial files in both cache backends. A pathname may
		// have been replaced since registration, so expiry must not delete it.
		if err := cache.SetStamp(key, uploadStamp(file)); err != nil {
			return http.StatusServiceUnavailable, err
		}
		if uploadLength == 0 {
			if err := cache.Complete(key); err != nil {
				return http.StatusServiceUnavailable, err
			}
			if err := d.RunAfterHook("upload", r.URL.Path, "", d.user); err != nil {
				return http.StatusInternalServerError, err
			}
		}

		basePath := "/" + strings.Trim(strings.TrimSpace(d.server.BaseURL), "/")
		if basePath == "/" {
			basePath = ""
		}

		w.Header().Set("Location", basePath+"/api/tus"+r.URL.EscapedPath())
		return http.StatusCreated, nil
	}))
}

func tusHeadHandler(cache UploadCache) handleFunc {
	return withUser(withUploadLock(cache, func(w http.ResponseWriter, r *http.Request, d *data) (int, error) {
		w.Header().Set("Cache-Control", "no-store")
		if !d.user.Perm.Create || !d.Check(r.URL.Path) {
			return http.StatusForbidden, nil
		}

		file, err := files.NewFileInfo(&files.FileOptions{
			Fs:         d.user.Fs,
			Path:       r.URL.Path,
			Modify:     d.user.Perm.Modify,
			Expand:     false,
			ReadHeader: d.server.TypeDetectionByHeader,
			Checker:    d,
		})
		if err != nil {
			return errToStatus(err), err
		}

		uploadLength, err := cache.GetLength(uploadKey(d, r.URL.Path))
		if err != nil {
			return http.StatusNotFound, err
		}
		if err := validateUploadFile(cache, uploadKey(d, r.URL.Path), file); err != nil {
			return http.StatusConflict, err
		}

		w.Header().Set("Upload-Offset", strconv.FormatInt(file.Size, 10))
		w.Header().Set("Upload-Length", strconv.FormatInt(uploadLength, 10))

		return http.StatusOK, nil
	}))
}

func tusPatchHandler(cache UploadCache) handleFunc {
	return withUser(withUploadLock(cache, func(w http.ResponseWriter, r *http.Request, d *data) (int, error) {
		status, err := tusPatchUpload(w, r, d, cache)
		// A rejected chunk is still a chunk the client is streaming: read what is
		// left of it so the answer reaches the client on a connection that stays
		// usable, instead of being lost to a reset.
		if status >= 400 {
			drainRequestBody(r)
		}
		return status, err
	}))
}

func tusPatchUpload(w http.ResponseWriter, r *http.Request, d *data, cache UploadCache) (status int, resultErr error) {
	if !d.user.Perm.Create || !d.Check(r.URL.Path) {
		return http.StatusForbidden, nil
	}
	if r.Header.Get("Content-Type") != "application/offset+octet-stream" {
		return http.StatusUnsupportedMediaType, nil
	}

	uploadOffset, err := getUploadOffset(r)
	if err != nil || uploadOffset < 0 {
		return http.StatusBadRequest, fmt.Errorf("invalid upload offset")
	}

	file, err := files.NewFileInfo(&files.FileOptions{
		Fs:         d.user.Fs,
		Path:       r.URL.Path,
		Modify:     d.user.Perm.Modify,
		Expand:     false,
		ReadHeader: d.server.TypeDetectionByHeader,
		Checker:    d,
	})

	switch {
	case errors.Is(err, afero.ErrFileNotFound):
		return http.StatusNotFound, nil
	case err != nil:
		return errToStatus(err), err
	}

	key := uploadKey(d, r.URL.Path)
	uploadLength, err := cache.GetLength(key)
	if err != nil {
		return http.StatusNotFound, err
	}
	if err := validateUploadFile(cache, key, file); err != nil {
		return http.StatusConflict, err
	}

	if uploadOffset > uploadLength {
		return http.StatusBadRequest, fmt.Errorf("upload offset %d exceeds declared length %d", uploadOffset, uploadLength)
	}

	// Prevent the upload from being evicted during the transfer
	stop := keepUploadActive(cache, key)
	defer stop()

	switch {
	case file.IsDir:
		return http.StatusBadRequest, fmt.Errorf("cannot upload to a directory %s", file.RealPath())
	case file.Size != uploadOffset:
		return http.StatusConflict, fmt.Errorf(
			"%s file size doesn't match the provided offset: %d",
			file.RealPath(),
			uploadOffset,
		)
	}

	openFile, err := d.user.Fs.OpenFile(r.URL.Path, os.O_WRONLY|os.O_APPEND, d.settings.FileMode)
	if err != nil {
		return http.StatusInternalServerError, fmt.Errorf("could not open file: %w", err)
	}
	defer openFile.Close()
	cacheActive := true
	// Refresh the binding even after a short or failed chunk so it can resume.
	defer func() {
		if !cacheActive {
			return
		}
		info, err := openFile.Stat()
		if err == nil {
			err = cache.SetStamp(key, fmt.Sprintf("%d:%d", info.ModTime().UnixNano(), info.Size()))
		}
		if err != nil {
			status, resultErr = http.StatusServiceUnavailable, err
		}
	}()

	_, err = openFile.Seek(uploadOffset, 0)
	if err != nil {
		return http.StatusInternalServerError, fmt.Errorf("could not seek file: %w", err)
	}

	// The server closes the request body once the handler returns; closing it
	// here would run before the caller gets to drain a rejected chunk, so every
	// status raised below this point would still cost the connection.
	//
	// Enforce the declared Upload-Length: never write more than the bytes
	// still expected for this upload. Reading one byte past the remainder
	// lets an over-length body be detected and rejected. Without this bound a
	// PATCH could stream arbitrary data to disk regardless of the length the
	// client declared when the upload was created.
	remaining := uploadLength - uploadOffset
	bytesWritten, err := io.Copy(openFile, io.LimitReader(r.Body, remaining+1))
	if err != nil {
		return http.StatusInternalServerError, fmt.Errorf("could not write to file: %w", err)
	}
	if bytesWritten > remaining {
		// The client sent more than it declared; roll this chunk back so the
		// file stays consistent with the tracked offset, and reject it.
		if truncErr := openFile.Truncate(uploadOffset); truncErr != nil {
			return http.StatusInternalServerError, fmt.Errorf("could not truncate file: %w", truncErr)
		}
		return http.StatusRequestEntityTooLarge, fmt.Errorf("upload exceeds declared length of %d bytes", uploadLength)
	}

	// Sync the file to ensure all data is written to storage
	// to prevent file corruption.
	if err := openFile.Sync(); err != nil {
		return http.StatusInternalServerError, fmt.Errorf("could not sync file: %w", err)
	}

	newOffset := uploadOffset + bytesWritten
	w.Header().Set("Upload-Offset", strconv.FormatInt(newOffset, 10))

	if newOffset >= uploadLength {
		if err := cache.Complete(key); err != nil {
			return http.StatusServiceUnavailable, err
		}
		cacheActive = false
		if err := d.RunAfterHook("upload", r.URL.Path, "", d.user); err != nil {
			return http.StatusInternalServerError, err
		}
	}

	return http.StatusNoContent, nil
}

func tusDeleteHandler(cache UploadCache) handleFunc {
	return withUser(withUploadLock(cache, func(_ http.ResponseWriter, r *http.Request, d *data) (int, error) {
		if r.URL.Path == "/" || !d.user.Perm.Delete {
			return http.StatusForbidden, nil
		}

		file, err := files.NewFileInfo(&files.FileOptions{
			Fs:         d.user.Fs,
			Path:       r.URL.Path,
			Modify:     d.user.Perm.Modify,
			Expand:     false,
			ReadHeader: d.server.TypeDetectionByHeader,
			Checker:    d,
		})
		if err != nil {
			return errToStatus(err), err
		}

		if file.IsDir {
			return http.StatusBadRequest, nil
		}
		_, err = cache.GetLength(uploadKey(d, r.URL.Path))
		if err != nil {
			return http.StatusNotFound, err
		}
		if err := validateUploadFile(cache, uploadKey(d, r.URL.Path), file); err != nil {
			return http.StatusConflict, err
		}

		err = d.user.Fs.Remove(r.URL.Path)
		if err != nil {
			return errToStatus(err), err
		}

		if err := cache.Complete(uploadKey(d, r.URL.Path)); err != nil {
			return http.StatusServiceUnavailable, err
		}

		return http.StatusNoContent, nil
	}))
}

func getUploadLength(r *http.Request) (int64, error) {
	uploadOffset, err := strconv.ParseInt(r.Header.Get("Upload-Length"), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid upload length: %w", err)
	}
	return uploadOffset, nil
}

func getUploadOffset(r *http.Request) (int64, error) {
	uploadOffset, err := strconv.ParseInt(r.Header.Get("Upload-Offset"), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid upload offset: %w", err)
	}
	return uploadOffset, nil
}
