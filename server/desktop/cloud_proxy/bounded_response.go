//go:build desktop

package cloud_proxy

import (
	"errors"
	"io"
	"net/http"
)

var errCloudResponseBodyInvalid = errors.New("cloud response body is missing, truncated, or too large")

// readBoundedCloudResponseBody enforces a hard aggregate response cap. A plain
// LimitReader(max) cannot distinguish an exact-sized body from an oversized
// body whose first max bytes happen to form valid JSON, so read max+1 and reject
// the latter. Declared length mismatches are protocol failures too.
func readBoundedCloudResponseBody(response *http.Response, maxBytes int64) ([]byte, error) {
	if response == nil || response.Body == nil || maxBytes <= 0 ||
		response.ContentLength < -1 || response.ContentLength > maxBytes {
		return nil, errCloudResponseBodyInvalid
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return nil, errCloudResponseBodyInvalid
	}
	if int64(len(body)) > maxBytes ||
		(response.ContentLength >= 0 && int64(len(body)) != response.ContentLength) {
		clear(body)
		return nil, errCloudResponseBodyInvalid
	}
	return body, nil
}
