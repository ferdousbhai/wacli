package wa

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ferdousbhai/wacli/internal/fsutil"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/socket"
	"go.mau.fi/whatsmeow/util/cbcutil"
	"go.mau.fi/whatsmeow/util/hkdfutil"
)

// WhatsApp's own limit for documents; the normal path streams to disk.
const MaxMediaDownloadSize = 2 * 1024 * 1024 * 1024

var directMediaBaseURL = "https://mmg.whatsapp.net"

var directMediaHTTPClient = newDirectMediaHTTPClient()

func newDirectMediaHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 10 * time.Second
	return &http.Client{
		Transport: transport,
	}
}

// WhatsApp writes encrypted media as padded ciphertext plus a 10-byte MAC before
// truncating and decrypting it in place.
const maxEncryptedMediaDownloadOverhead = 16 + 10

func MediaTypeFromString(mediaType string) (whatsmeow.MediaType, error) {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "image":
		return whatsmeow.MediaImage, nil
	case "video":
		return whatsmeow.MediaVideo, nil
	case "gif":
		// WhatsApp gifs are video messages with a gif-playback hint;
		// they are stored and encrypted as regular videos.
		return whatsmeow.MediaVideo, nil
	case "audio":
		return whatsmeow.MediaAudio, nil
	case "document":
		return whatsmeow.MediaDocument, nil
	case "sticker":
		return whatsmeow.MediaImage, nil
	default:
		return "", fmt.Errorf("unsupported media type: %s", mediaType)
	}
}

// DownloadMediaToFile downloads media to targetPath, which the caller owns
// (a private temp path; it is removed on failure).
func (c *Client) DownloadMediaToFile(ctx context.Context, directPath string, encFileHash, fileHash, mediaKey []byte, fileLength uint64, mediaType, targetPath string) (int64, error) {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if !ready(cli) {
		return 0, ErrNotConnected
	}
	if strings.TrimSpace(directPath) == "" {
		return 0, fmt.Errorf("direct path is required")
	}
	mt, err := MediaTypeFromString(mediaType)
	if err != nil {
		return 0, err
	}
	if err := checkMediaLength(fileLength); err != nil {
		return 0, err
	}
	return writeTarget(targetPath, func(f *os.File) (int64, error) {
		limited := &limitedDownloadFile{File: f, max: MaxMediaDownloadSize + maxEncryptedMediaDownloadOverhead, userMax: MaxMediaDownloadSize}
		if err := cli.DownloadMediaWithPathToFile(ctx, directPath, encFileHash, fileHash, mediaKey, mt, "", false, limited); err != nil {
			return 0, err
		}
		info, err := f.Stat()
		if err != nil {
			return 0, err
		}
		if info.Size() > MaxMediaDownloadSize {
			return 0, fmt.Errorf("media too large; maximum download size is %d bytes", int64(MaxMediaDownloadSize))
		}
		return info.Size(), nil
	})
}

// writeTarget creates targetPath, lets fill write it, syncs it, and removes
// it again if anything failed.
func writeTarget(targetPath string, fill func(f *os.File) (int64, error)) (int64, error) {
	if err := fsutil.EnsureWritableDir(filepath.Dir(targetPath)); err != nil {
		return 0, fmt.Errorf("create output dir: %w", err)
	}
	f, err := os.OpenFile(targetPath, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0o600)
	if err != nil {
		return 0, err
	}
	n, err := fill(f)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(targetPath)
		return 0, err
	}
	return n, nil
}

// DownloadRetriedMediaToFile accepts a path from a successfully decrypted retry
// notification. Re-uploaded ciphertext can differ, but the original plaintext
// digest and media-key HMAC must still authenticate the downloaded bytes.
func (c *Client) DownloadRetriedMediaToFile(ctx context.Context, directPath string, fileHash, mediaKey []byte, fileLength uint64, mediaType, targetPath string) (int64, error) {
	if len(fileHash) != sha256.Size || len(mediaKey) != 32 {
		return 0, fmt.Errorf("media retry requires the original SHA-256 digest and 32-byte media key")
	}
	// The streaming downloader treats a nil ciphertext hash as unencrypted media
	// and skips MAC extraction. The direct downloader preserves that validation.
	return DownloadMediaDirectToFile(ctx, directPath, nil, fileHash, mediaKey, fileLength, mediaType, targetPath)
}

func DownloadMediaDirectToFile(ctx context.Context, directPath string, encFileHash, fileHash, mediaKey []byte, fileLength uint64, mediaType string, targetPath string) (int64, error) {
	if strings.TrimSpace(directPath) == "" {
		return 0, fmt.Errorf("direct path is required")
	}
	mt, err := MediaTypeFromString(mediaType)
	if err != nil {
		return 0, err
	}
	mediaURL, err := directMediaURL(directPath, encFileHash, mt)
	if err != nil {
		return 0, err
	}
	if err := checkMediaLength(fileLength); err != nil {
		return 0, err
	}
	return writeTarget(targetPath, func(f *os.File) (int64, error) {
		return downloadAndDecryptDirect(ctx, mediaURL, encFileHash, fileHash, mediaKey, fileLength, mt, f)
	})
}

func directMediaURL(directPath string, encFileHash []byte, mediaType whatsmeow.MediaType) (string, error) {
	path := strings.TrimSpace(directPath)
	if strings.Contains(path, "://") || strings.HasPrefix(path, "//") {
		return "", fmt.Errorf("media download path must be a WhatsApp direct path, not a URL")
	}
	if !strings.HasPrefix(path, "/") {
		return "", fmt.Errorf("media download path does not start with slash: %s", path)
	}
	mediaURL := strings.TrimRight(directMediaBaseURL, "/") + path
	if len(encFileHash) > 0 {
		mediaURL = appendDirectMediaQuery(mediaURL, "hash", base64.URLEncoding.EncodeToString(encFileHash))
	}
	if mmsType := directMediaMMSType(mediaType); mmsType != "" {
		mediaURL = appendDirectMediaQuery(mediaURL, "mms-type", mmsType)
	}
	return appendDirectMediaQuery(mediaURL, "__wa-mms", ""), nil
}

func appendDirectMediaQuery(mediaURL, key, value string) string {
	sep := "?"
	if strings.Contains(mediaURL, "?") {
		sep = "&"
	}
	if strings.HasSuffix(mediaURL, "?") || strings.HasSuffix(mediaURL, "&") {
		sep = ""
	}
	return mediaURL + sep + key + "=" + value
}

func directMediaMMSType(mediaType whatsmeow.MediaType) string {
	switch mediaType {
	case whatsmeow.MediaImage:
		return "image"
	case whatsmeow.MediaAudio:
		return "audio"
	case whatsmeow.MediaVideo:
		return "video"
	case whatsmeow.MediaDocument:
		return "document"
	default:
		return ""
	}
}

// downloadAndDecryptDirect downloads encrypted media into f and decrypts it
// there, checking the hashes and MAC on the way. It streams: memory use
// doesn't grow with the file.
func downloadAndDecryptDirect(ctx context.Context, mediaURL string, encFileHash, fileHash, mediaKey []byte, fileLength uint64, mediaType whatsmeow.MediaType, f *os.File) (int64, error) {
	encSum := sha256.New()
	n, err := downloadDirect(ctx, mediaURL, io.MultiWriter(f, encSum))
	if err != nil {
		return 0, err
	}
	if n <= mediaHMACLength {
		return 0, whatsmeow.ErrTooShortFile
	}
	if len(encFileHash) == sha256.Size && !bytes.Equal(encSum.Sum(nil), encFileHash) {
		return 0, whatsmeow.ErrInvalidMediaEncSHA256
	}
	// The last bytes are the MAC; the rest is the ciphertext.
	mac := make([]byte, mediaHMACLength)
	if _, err := f.ReadAt(mac, n-mediaHMACLength); err != nil {
		return 0, err
	}
	if err := f.Truncate(n - mediaHMACLength); err != nil {
		return 0, err
	}
	iv, cipherKey, macKey := directMediaKeys(mediaKey, mediaType)
	h := hmac.New(sha256.New, macKey)
	h.Write(iv)
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	if _, err := io.Copy(h, f); err != nil {
		return 0, err
	}
	if !hmac.Equal(h.Sum(nil)[:mediaHMACLength], mac) {
		return 0, whatsmeow.ErrInvalidMediaHMAC
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	if err := cbcutil.DecryptFile(cipherKey, iv, f); err != nil {
		return 0, fmt.Errorf("failed to decrypt file: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		return 0, err
	}
	if fileLength > 0 && uint64(info.Size()) != fileLength {
		return 0, fmt.Errorf("%w: expected %d, got %d", whatsmeow.ErrFileLengthMismatch, fileLength, info.Size())
	}
	if len(fileHash) == sha256.Size {
		sum := sha256.New()
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return 0, err
		}
		if _, err := io.Copy(sum, f); err != nil {
			return 0, err
		}
		if !bytes.Equal(sum.Sum(nil), fileHash) {
			return 0, whatsmeow.ErrInvalidMediaSHA256
		}
	}
	return info.Size(), nil
}

// downloadDirect streams the media at mediaURL into w, up to the size cap.
func downloadDirect(ctx context.Context, mediaURL string, w io.Writer) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, mediaURL, nil)
	if err != nil {
		return 0, fmt.Errorf("prepare media request: %w", err)
	}
	req.Header.Set("Origin", socket.Origin)
	req.Header.Set("Referer", socket.Origin+"/")
	resp, err := directMediaHTTPClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, whatsmeow.DownloadHTTPError{Response: resp}
	}
	const limit = MaxMediaDownloadSize + maxEncryptedMediaDownloadOverhead
	n, err := io.Copy(w, io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return n, err
	}
	if n > limit {
		return n, fmt.Errorf("media too large; maximum download size is %d bytes", int64(MaxMediaDownloadSize))
	}
	return n, nil
}

func directMediaKeys(mediaKey []byte, appInfo whatsmeow.MediaType) (iv, cipherKey, macKey []byte) {
	expanded := hkdfutil.SHA256(mediaKey, nil, []byte(appInfo), 112)
	return expanded[:16], expanded[16:48], expanded[48:80]
}

const mediaHMACLength = 10

type limitedDownloadFile struct {
	*os.File
	max     int64
	userMax int64
}

func (f *limitedDownloadFile) Write(p []byte) (int, error) {
	off, err := f.File.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, err
	}
	if off+int64(len(p)) > f.max {
		return 0, f.limitError()
	}
	return f.File.Write(p)
}

func (f *limitedDownloadFile) WriteAt(p []byte, off int64) (int, error) {
	if off+int64(len(p)) > f.max {
		return 0, f.limitError()
	}
	return f.File.WriteAt(p, off)
}

func (f *limitedDownloadFile) ReadFrom(r io.Reader) (int64, error) {
	off, err := f.File.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, err
	}
	remaining := max(f.max-off, 0)
	n, err := io.Copy(f.File, io.LimitReader(r, remaining))
	if err != nil {
		return n, err
	}
	var probe [1]byte
	extra, err := r.Read(probe[:])
	if extra > 0 {
		return n, f.limitError()
	}
	if err != nil && err != io.EOF {
		return n, err
	}
	return n, nil
}

func (f *limitedDownloadFile) Truncate(size int64) error {
	if size > f.max {
		return f.limitError()
	}
	return f.File.Truncate(size)
}

func (f *limitedDownloadFile) limitError() error {
	max := f.userMax
	if max <= 0 {
		max = f.max
	}
	return fmt.Errorf("media too large; maximum download size is %d bytes", max)
}

// checkMediaLength rejects media WhatsApp says is larger than wacli downloads.
func checkMediaLength(fileLength uint64) error {
	if fileLength > MaxMediaDownloadSize {
		return fmt.Errorf("media too large (%d bytes); maximum download size is %d bytes", fileLength, int64(MaxMediaDownloadSize))
	}
	return nil
}
