package updatecdn

import (
	"errors"
	"io"
	"mime"
	"net/http"
)

// 管理后台上传包的 HTTP 约定，面板（telesrv-admin）与 admin API 共用：请求体就是包的
// 原始字节（application/octet-stream，不用 multipart），按流读取，从不整个读进内存。

// PackageUploadSlack 是上传请求体在包上限之外允许的余量。暂存本身按包上限截断，
// 这里只是给 MaxBytesReader 一个不会先于暂存触发的上限。
const PackageUploadSlack = 1 << 20

// ErrUploadContentType 表示上传请求不是 application/octet-stream。
var ErrUploadContentType = errors.New("upload the package as application/octet-stream")

// PackageUploadBody checks a package upload request and returns its body,
// capped at MaxDesktopPackageSize plus PackageUploadSlack. A request that is
// not application/octet-stream reports ErrUploadContentType (answer 415); one
// that declares a length over MaxDesktopPackageSize reports
// ErrStagedPackageTooLarge (answer 413) before anything is read.
func PackageUploadBody(w http.ResponseWriter, r *http.Request) (io.ReadCloser, error) {
	if mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mediaType != "application/octet-stream" {
		return nil, ErrUploadContentType
	}
	if r.ContentLength > MaxDesktopPackageSize {
		return nil, ErrStagedPackageTooLarge
	}
	return http.MaxBytesReader(w, r.Body, MaxDesktopPackageSize+PackageUploadSlack), nil
}
