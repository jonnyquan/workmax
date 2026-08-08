//go:build desktop

package cloud_proxy

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestReadBoundedCloudResponseBodyHardLimit(t *testing.T) {
	tests := []struct {
		name          string
		body          string
		contentLength int64
		max           int64
		want          string
		wantErr       bool
	}{
		{name: "exact limit", body: "abcd", contentLength: 4, max: 4, want: "abcd"},
		{name: "chunked over limit", body: "abcde", contentLength: -1, max: 4, wantErr: true},
		{name: "declared over limit", body: "abcd", contentLength: 5, max: 4, wantErr: true},
		{name: "declared truncated", body: "abc", contentLength: 4, max: 4, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := &http.Response{
				Body:          io.NopCloser(strings.NewReader(test.body)),
				ContentLength: test.contentLength,
			}
			got, err := readBoundedCloudResponseBody(response, test.max)
			if test.wantErr {
				if !errors.Is(err, errCloudResponseBodyInvalid) {
					t.Fatalf("error = %v, want errCloudResponseBodyInvalid", err)
				}
				return
			}
			if err != nil || string(got) != test.want {
				t.Fatalf("body = %q, err=%v, want %q", got, err, test.want)
			}
		})
	}
}
