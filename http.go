package tgtg

import (
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const (
	maxAPIResponseBytes      int64 = 16 << 20
	maxDataDomeResponseBytes int64 = 1 << 20
)

func readLimitedBody(r io.Reader, limit int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("response body exceeds %d bytes", limit)
	}
	return body, nil
}

// readResponseBody bounds decoded bytes even when a custom transport leaves
// gzip decoding to us. The caller owns and closes resp.Body.
func readResponseBody(resp *http.Response, limit int64) ([]byte, error) {
	var reader io.Reader = resp.Body
	switch strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding"))) {
	case "", "identity":
	case "gzip":
		gz, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("gzip reader: %w", err)
		}
		defer gz.Close()
		reader = gz
	default:
		return nil, fmt.Errorf("unsupported content encoding %q", resp.Header.Get("Content-Encoding"))
	}
	return readLimitedBody(reader, limit)
}
