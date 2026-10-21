package engine

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
)

type Engine struct {
	client    *torrent.Client
	dataDir   string
	mu        sync.RWMutex
	publicURL string
	magnets   map[metainfo.Hash]string
}

// DisallowedScriptExtensions defines script and shell file extensions that are blocked for security
var DisallowedScriptExtensions = map[string]string{
	".bat":     "Windows Batch script",
	".cmd":     "Windows Command script",
	".btm":     "Batch script",
	".sh":      "Unix Shell script",
	".bash":    "Bash script",
	".zsh":     "Zsh script",
	".csh":     "Csh script",
	".ksh":     "Ksh script",
	".command": "Command script",
	".ps1":     "PowerShell script",
	".ps1xml":  "PowerShell data script",
	".ps2":     "PowerShell script",
	".ps2xml":  "PowerShell data script",
	".psc1":    "PowerShell console file",
	".psc2":    "PowerShell console file",
	".msh":     "Monad shell script",
	".msh1":    "Monad shell script",
	".msh2":    "Monad shell script",
	".mshxml":  "Monad script file",
	".vbs":     "VBScript file",
	".vbe":     "Encoded VBScript",
	".js":      "JavaScript script file",
	".jse":     "Encoded JavaScript",
	".wsf":     "Windows Script File",
	".wsh":     "Windows Script Host settings",
	".hta":     "HTML Application executable script",
	".scr":     "Windows Script/Screensaver file",
	".pif":     "Program Information File shortcut",
	".com":     "DOS Command file",
	".cpl":     "Control Panel executable script",
	".reg":     "Registry modification script",
	".inf":     "Setup information script",
	".ins":     "Internet communication settings",
	".isp":     "Internet service settings",
	".au3":     "AutoIt script",
	".ahk":     "AutoHotkey script",
}

// IsDisallowedScript checks if a file is a restricted script/batch file
func IsDisallowedScript(name string) (bool, string) {
	ext := strings.ToLower(filepath.Ext(name))
	if desc, blocked := DisallowedScriptExtensions[ext]; blocked {
		return true, fmt.Sprintf("Restricted: %s (%s) is not permitted for security reasons", desc, ext)
	}
	return false, ""
}

// MaxFileSizeBytes defines the 5 GB maximum file size cap for cloud ingestion
const MaxFileSizeBytes int64 = 5 * 1024 * 1024 * 1024

type FileMeta struct {
	Index         int    `json:"index"`
	Name          string `json:"name"`
	Path          string `json:"path"`
	Length        int64  `json:"length"`
	FormattedSize string `json:"formattedSize"`
	MimeType      string `json:"mimeType"`
	IsVideo       bool   `json:"isVideo"`
	IsAudio       bool   `json:"isAudio"`
	IsDisallowed  bool   `json:"isDisallowed"`
	DisallowedMsg string `json:"disallowedMsg,omitempty"`
	IsTooLarge    bool   `json:"isTooLarge"`
	TooLargeMsg   string `json:"tooLargeMsg,omitempty"`
	StreamURL     string `json:"streamUrl"`
	DownloadURL   string `json:"downloadUrl"`
}

type TorrentMeta struct {
	InfoHash      string     `json:"infoHash"`
	Name          string     `json:"name"`
	TotalLength   int64      `json:"totalLength"`
	FormattedSize string     `json:"formattedSize"`
	NumPeers      int        `json:"numPeers"`
	Files         []FileMeta `json:"files"`
}

func NewEngine(dataDir, publicURL string) (*Engine, error) {
	if dataDir == "" {
		dataDir = filepath.Join(os.TempDir(), "cloud-tor-cache")
	}
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create data dir: %w", err)
	}

	cfg := torrent.NewDefaultClientConfig()
	cfg.DataDir = dataDir
	cfg.DefaultStorage = storage.NewFile(dataDir)

	// Dynamic ephemeral port binding to prevent port conflicts
	cfg.SetListenAddr(":0")

	// Resource bounds for cloud containers (512MB RAM)
	cfg.EstablishedConnsPerTorrent = 25
	cfg.NoUpload = true // Do not waste RAM and upstream bandwidth on seeding
	cfg.DisableUTP = false
	cfg.DisableTCP = false

	client, err := torrent.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create torrent client: %w", err)
	}

	return &Engine{
		client:    client,
		dataDir:   dataDir,
		publicURL: strings.TrimRight(publicURL, "/"),
		magnets:   make(map[metainfo.Hash]string),
	}, nil
}

func (e *Engine) Close() {
	if e.client != nil {
		e.client.Close()
	}
}

func GetMimeType(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".mkv":
		return "video/x-matroska"
	case ".mp4":
		return "video/mp4"
	case ".webm":
		return "video/webm"
	case ".avi":
		return "video/x-msvideo"
	case ".mov":
		return "video/quicktime"
	case ".m4v":
		return "video/x-m4v"
	case ".ts":
		return "video/mp2t"
	case ".flv":
		return "video/x-flv"
	case ".wmv":
		return "video/x-ms-wmv"
	case ".mp3":
		return "audio/mpeg"
	case ".aac":
		return "audio/aac"
	case ".flac":
		return "audio/flac"
	case ".m4a":
		return "audio/mp4"
	case ".ogg":
		return "audio/ogg"
	case ".wav":
		return "audio/wav"
	case ".opus":
		return "audio/opus"
	default:
		mt := mime.TypeByExtension(ext)
		if mt != "" {
			return mt
		}
		return "application/octet-stream"
	}
}

func FormatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

func isVideoExt(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".mkv", ".mp4", ".avi", ".webm", ".mov", ".m4v", ".flv", ".ts", ".wmv":
		return true
	default:
		return false
	}
}

func isAudioExt(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".mp3", ".wav", ".ogg", ".flac", ".m4a", ".aac", ".opus":
		return true
	default:
		return false
	}
}

// ResolveMagnet parses magnet URI or info-hash, downloads metadata within timeout, and returns file list.
func (e *Engine) ResolveMagnet(ctx context.Context, uri string) (*TorrentMeta, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	var t *torrent.Torrent
	var err error

	uri = strings.TrimSpace(uri)
	if strings.HasPrefix(uri, "magnet:?") {
		t, err = e.client.AddMagnet(uri)
		if err != nil {
			return nil, fmt.Errorf("failed to add magnet: %w", err)
		}
	} else if len(uri) == 40 {
		var h metainfo.Hash
		if err := h.FromHexString(uri); err != nil {
			return nil, fmt.Errorf("invalid info-hash: %w", err)
		}
		var exists bool
		t, exists = e.client.Torrent(h)
		if !exists {
			t, _ = e.client.AddTorrentInfoHash(h)
		}
	} else {
		return nil, fmt.Errorf("invalid magnet link or info-hash format")
	}

	// Wait for metadata
	select {
	case <-t.GotInfo():
		// Metadata received successfully
	case <-ctx.Done():
		return nil, fmt.Errorf("timed out waiting for torrent metadata: check if magnet has seeds")
	}

	// ZERO-DISK / ZERO-RAM IDLE STEP:
	// Deselect all files so the client does NOT download content until a stream request arrives
	for _, f := range t.Files() {
		f.SetPriority(torrent.PiecePriorityNone)
	}

	info := t.Info()
	hashStr := t.InfoHash().HexString()
	e.magnets[t.InfoHash()] = uri

	name := t.Name()
	if name == "" && info != nil {
		name = info.Name
	}

	files := make([]FileMeta, len(t.Files()))
	for i, f := range t.Files() {
		path := f.Path()
		filename := filepath.Base(path)
		mimeType := GetMimeType(filename)

		isDisallowed, disallowMsg := IsDisallowedScript(filename)
		isVideo := isVideoExt(path) || strings.HasPrefix(mimeType, "video/")
		isAudio := isAudioExt(path) || strings.HasPrefix(mimeType, "audio/")

		isTooLarge := f.Length() > MaxFileSizeBytes
		tooLargeMsg := ""
		if isTooLarge {
			tooLargeMsg = fmt.Sprintf("Exceeds 5 GB maximum limit (%s)", FormatBytes(f.Length()))
		}

		escapedName := url.PathEscape(filename)
		var streamURL, downloadURL string
		if !isDisallowed && !isTooLarge {
			streamURL = fmt.Sprintf("%s/api/stream/%s/%d/%s", e.publicURL, hashStr, i, escapedName)
			downloadURL = fmt.Sprintf("%s/api/stream/%s/%d/%s?download=1", e.publicURL, hashStr, i, escapedName)
		}

		files[i] = FileMeta{
			Index:         i,
			Name:          filename,
			Path:          path,
			Length:        f.Length(),
			FormattedSize: FormatBytes(f.Length()),
			MimeType:      mimeType,
			IsVideo:       isVideo,
			IsAudio:       isAudio,
			IsDisallowed:  isDisallowed,
			DisallowedMsg: disallowMsg,
			IsTooLarge:    isTooLarge,
			TooLargeMsg:   tooLargeMsg,
			StreamURL:     streamURL,
			DownloadURL:   downloadURL,
		}
	}

	totalLen := t.Length()

	return &TorrentMeta{
		InfoHash:      hashStr,
		Name:          name,
		TotalLength:   totalLen,
		FormattedSize: FormatBytes(totalLen),
		NumPeers:      len(t.PeerConns()),
		Files:         files,
	}, nil
}

// GetStreamReader returns an io.ReadSeekCloser on the target file with responsive piece priority.
// If the torrent is not active, it automatically reactivates via stored magnet or DHT.
func (e *Engine) GetStreamReader(infoHashStr string, fileIndex int, reqCtx context.Context) (io.ReadSeekCloser, *torrent.File, error) {
	var h metainfo.Hash
	if err := h.FromHexString(infoHashStr); err != nil {
		return nil, nil, fmt.Errorf("invalid info-hash: %w", err)
	}

	e.mu.Lock()
	t, exists := e.client.Torrent(h)
	if !exists {
		if magnet, ok := e.magnets[h]; ok {
			t, _ = e.client.AddMagnet(magnet)
		} else {
			t, _ = e.client.AddTorrentInfoHash(h)
		}
	}
	e.mu.Unlock()

	// Wait for metadata
	select {
	case <-t.GotInfo():
	case <-reqCtx.Done():
		return nil, nil, reqCtx.Err()
	case <-time.After(35 * time.Second):
		return nil, nil, fmt.Errorf("timed out waiting for torrent metadata: check seeds")
	}

	files := t.Files()
	if fileIndex < 0 || fileIndex >= len(files) {
		return nil, nil, fmt.Errorf("invalid file index: %d", fileIndex)
	}

	f := files[fileIndex]
	filename := filepath.Base(f.Path())
	if isDisallowed, reason := IsDisallowedScript(filename); isDisallowed {
		return nil, nil, fmt.Errorf("file access restricted: %s", reason)
	}

	f.Download()

	// Create seekable reader bound to request context
	reader := f.NewReader()
	reader.SetContext(reqCtx)
	reader.SetResponsive()
	reader.SetReadahead(16 * 1024 * 1024) // 16 MB sliding window readahead

	return reader, f, nil
}

// GetFile retrieves metadata of a file within a resolved torrent.
func (e *Engine) GetFile(infoHashStr string, fileIndex int) (*torrent.File, error) {
	var h metainfo.Hash
	if err := h.FromHexString(infoHashStr); err != nil {
		return nil, fmt.Errorf("invalid info-hash: %w", err)
	}

	e.mu.RLock()
	t, exists := e.client.Torrent(h)
	e.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("torrent not found: please resolve magnet first")
	}

	files := t.Files()
	if fileIndex < 0 || fileIndex >= len(files) {
		return nil, fmt.Errorf("invalid file index: %d", fileIndex)
	}

	return files[fileIndex], nil
}

// PruneTorrent drops the torrent from active DHT sessions and purges temporary files from disk
func (e *Engine) PruneTorrent(infoHashStr string) {
	var h metainfo.Hash
	if err := h.FromHexString(infoHashStr); err != nil {
		return
	}

	e.mu.Lock()
	if t, exists := e.client.Torrent(h); exists {
		t.Drop()
	}
	delete(e.magnets, h)
	e.mu.Unlock()

	// Clean up any files from local disk cache
	pattern := filepath.Join(e.dataDir, infoHashStr+"*")
	matches, _ := filepath.Glob(pattern)
	for _, m := range matches {
		_ = os.RemoveAll(m)
	}
}

