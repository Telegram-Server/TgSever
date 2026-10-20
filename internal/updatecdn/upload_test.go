package updatecdn

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPackageUploadBodyChecksTheRequest(t *testing.T) {
	cases := []struct {
		name          string
		contentType   string
		contentLength int64
		want          error
	}{
		{name: "octet-stream", contentType: "application/octet-stream", contentLength: 7},
		{name: "octet-stream with parameters", contentType: "application/octet-stream; charset=binary", contentLength: 7},
		{name: "unknown length", contentType: "application/octet-stream", contentLength: -1},
		{name: "exactly the limit", contentType: "application/octet-stream", contentLength: MaxDesktopPackageSize},
		{name: "multipart", contentType: "multipart/form-data; boundary=x", contentLength: 7, want: ErrUploadContentType},
		{name: "no content type", contentLength: 7, want: ErrUploadContentType},
		{name: "declared over the limit", contentType: "application/octet-stream", contentLength: MaxDesktopPackageSize + 1, want: ErrStagedPackageTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/upload", strings.NewReader("package"))
			if tc.contentType != "" {
				req.Header.Set("Content-Type", tc.contentType)
			}
			req.ContentLength = tc.contentLength
			body, err := PackageUploadBody(httptest.NewRecorder(), req)
			if !errors.Is(err, tc.want) {
				t.Fatalf("PackageUploadBody() error = %v, want %v", err, tc.want)
			}
			if tc.want != nil {
				return
			}
			got, err := io.ReadAll(body)
			if err != nil || string(got) != "package" {
				t.Fatalf("body = %q, %v; want the request body", got, err)
			}
		})
	}
}

// 未声明长度的请求体在包上限加少量余量处被截断，暂存的上限先于它生效。
func TestPackageUploadBodyCapsAnUndeclaredBody(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/upload", io.LimitReader(zeroReader{}, MaxDesktopPackageSize+PackageUploadSlack+1))
	req.Header.Set("Content-Type", "application/octet-stream")
	req.ContentLength = -1
	body, err := PackageUploadBody(httptest.NewRecorder(), req)
	if err != nil {
		t.Fatal(err)
	}
	n, err := io.Copy(io.Discard, body)
	var tooLarge *http.MaxBytesError
	if !errors.As(err, &tooLarge) || n != MaxDesktopPackageSize+PackageUploadSlack {
		t.Fatalf("read %d bytes, error %v; want the body cut at %d", n, err, MaxDesktopPackageSize+PackageUploadSlack)
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}
