package api

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Upload limits the desktop app enforces before sending a file to a bot.
const (
	MaxUploadBytes      = 25 << 20  // most files
	MaxVideoUploadBytes = 200 << 20 // video
	chunkBytes          = 4 << 20   // gateway chunk size (upload and download)
)

var videoExt = map[string]bool{".mp4": true, ".mov": true, ".m4v": true, ".webm": true, ".mkv": true, ".avi": true, ".mpeg": true, ".mpg": true}

// UploadLimit returns the Grok Bot size limit for a file name.
func UploadLimit(name string) int64 {
	if videoExt[strings.ToLower(filepath.Ext(name))] {
		return MaxVideoUploadBytes
	}
	return MaxUploadBytes
}

// TooLarge reports a file over a size limit.
type TooLarge struct {
	Name  string
	Size  int64
	Limit int64
}

func (t *TooLarge) Error() string {
	return fmt.Sprintf("%s is %s, over the %s limit", t.Name, HumanSize(t.Size), HumanSize(t.Limit))
}

func HumanSize(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// Upload sends bytes to the box and returns the path to pass as an attachment.
func (a *API) Upload(ctx context.Context, agentID, filename string, b []byte) (string, error) {
	return a.UploadStream(ctx, agentID, filename, strings.NewReader(string(b)), int64(len(b)))
}

// UploadStream streams size bytes from r in 4 MiB chunks (uploadAttachmentChunk) without buffering the file.
func (a *API) UploadStream(ctx context.Context, agentID, filename string, r io.Reader, size int64) (string, error) {
	if limit := UploadLimit(filename); size > limit {
		return "", &TooLarge{filename, size, limit}
	}
	id := strconv.FormatInt(time.Now().UnixNano(), 36)
	buf := make([]byte, chunkBytes)
	for off := int64(0); ; {
		n, err := io.ReadFull(r, buf)
		if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
			return "", err
		}
		if n == 0 && off < size {
			return "", fmt.Errorf("%s: got %d of %d bytes", filename, off, size)
		}
		var res struct {
			CommittedPath string `json:"committedPath"`
		}
		if err := a.Call(ctx, "uploadAttachmentChunk", map[string]any{
			"agentId": agentID, "filename": filename, "uploadId": id,
			"offset": off, "totalSize": size, "bytesBase64": base64.StdEncoding.EncodeToString(buf[:n]),
		}, &res); err != nil {
			return "", err
		}
		off += int64(n)
		if res.CommittedPath != "" {
			return res.CommittedPath, nil
		}
		if off >= size {
			return "", errors.New("upload finished without a committed path")
		}
	}
}

// FileInfo describes a file read from the box.
type FileInfo struct {
	Name string `json:"name"`
	Mime string `json:"mime,omitempty"`
	Size int64  `json:"size"`
}

// ReadFile copies a bot file to w. Box paths (and file:// URLs) are read with readAttachmentChunk; plain
// http(s) URLs are fetched directly. maxBytes > 0 refuses (before copying anything) files that are larger.
// onStart (optional) runs once the name/size/mime are known and before the first byte is written.
func (a *API) ReadFile(ctx context.Context, agentID string, f File, w io.Writer, maxBytes int64, onStart ...func(FileInfo)) (*FileInfo, error) {
	start := func(i *FileInfo) {
		for _, fn := range onStart {
			fn(*i)
		}
	}
	if f.Path == "" {
		return a.fetchURL(ctx, f, w, maxBytes, start)
	}
	info := &FileInfo{Name: f.Name}
	for off := int64(0); ; {
		var c *struct {
			BytesBase64  string  `json:"bytesBase64"`
			Mime         *string `json:"mime"`
			TotalSize    float64 `json:"totalSize"`
			ResolvedName string  `json:"resolvedName"`
		}
		args := map[string]any{"path": f.Path, "offset": off, "length": chunkBytes}
		if agentID != "" {
			args["agentId"] = agentID
		}
		if err := a.Call(ctx, "readAttachmentChunk", args, &c); err != nil {
			return nil, err
		}
		if c == nil {
			return nil, fmt.Errorf("%s: not found on the box", f.Path)
		}
		if off == 0 {
			info.Size = int64(c.TotalSize)
			if c.Mime != nil {
				info.Mime = *c.Mime
			}
			if c.ResolvedName != "" {
				info.Name = c.ResolvedName
			}
			if maxBytes > 0 && info.Size > maxBytes {
				return info, &TooLarge{info.Name, info.Size, maxBytes}
			}
			start(info)
		}
		b, err := base64.StdEncoding.DecodeString(c.BytesBase64)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(b); err != nil {
			return nil, err
		}
		off += int64(len(b))
		if len(b) == 0 || off >= info.Size {
			return info, nil
		}
	}
}

func (a *API) fetchURL(ctx context.Context, f File, w io.Writer, maxBytes int64, start func(*FileInfo)) (*FileInfo, error) {
	if !strings.HasPrefix(f.URL, "https://") && !strings.HasPrefix(f.URL, "http://") {
		return nil, fmt.Errorf("cannot read %q", f.URL)
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", f.URL, nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("%s: HTTP %d", f.URL, res.StatusCode)
	}
	info := &FileInfo{Name: f.Name, Mime: res.Header.Get("content-type"), Size: res.ContentLength}
	if maxBytes > 0 && info.Size > maxBytes {
		return info, &TooLarge{info.Name, info.Size, maxBytes}
	}
	start(info)
	var r io.Reader = res.Body
	if maxBytes > 0 {
		r = io.LimitReader(res.Body, maxBytes+1)
	}
	n, err := io.Copy(w, r)
	if maxBytes > 0 && n > maxBytes {
		return info, &TooLarge{info.Name, n, maxBytes}
	}
	info.Size = n
	return info, err
}
