// Package apkinfo 从 Android APK 中读出包名、版本号与签名证书指纹，供更新服务
// （internal/updatecdn）在发布安卓安装包时做预检：发现“换了签名密钥”“传错了应用”
// “版本号没涨”这类运营失误。
//
// 本包只做结构解析，不做任何签名的密码学校验：签名块里的摘要与签名都不验证，读出的
// 证书指纹只说明“APK 声称由谁签名”。真正的签名校验由 Android 在安装时完成，被篡改的
// 包装不上去；这里的预检只为让运营者在发布前就看到问题。
//
// 输入是运营者上传的任意文件，所有长度字段都先做边界检查再使用：AndroidManifest.xml
// 解压后最多读 maxManifestSize 字节，签名块最多读 maxSigningBlockSize 字节，任何畸形
// 输入都只返回错误，不 panic，不按文件里声明的长度分配内存。
package apkinfo

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"os"
)

const (
	// maxManifestSize 是 AndroidManifest.xml 解压后的大小上限。真实应用的清单通常只有
	// 几十 KB，4 MiB 足以容纳任何合理的清单，又能挡住压缩炸弹。
	maxManifestSize = 4 << 20
	// maxSigningBlockSize 是 APK Signing Block 的大小上限（含首尾的长度字段与魔数）。
	maxSigningBlockSize = 32 << 20
)

// Info 是从 APK 读出的元数据。
type Info struct {
	PackageName string // AndroidManifest <manifest package="...">
	VersionCode int64  // (versionCodeMajor << 32) | versionCode；没有 versionCodeMajor 时就是 versionCode
	VersionName string // android:versionName，必须是字面字符串
	MinSDK      int    // <uses-sdk android:minSdkVersion>，没有时为 0
	// SignerCertSHA256 是 APK 签名块里第一个签名者的第一张证书（DER）的 SHA-256，小写十六进制。
	// v3 签名块（ID 0xf05368c0）优先，其次 v2（ID 0x7109871a）。
	SignerCertSHA256 string
	SigningScheme    int // 3 或 2
}

var (
	// ErrNotAPK 表示输入不是可解析的 APK：不是 zip、没有 AndroidManifest.xml，
	// 或清单的二进制 XML 无法解析、缺少必需字段。
	ErrNotAPK = errors.New("apkinfo: not an APK")
	// ErrUnsigned 表示 APK 没有 APK Signing Block，或其中既没有 v3 也没有 v2 签名者。
	// 只有 v1（JAR）签名的 APK 也归为此错误。
	ErrUnsigned = errors.New("apkinfo: APK has no v2/v3 signature")
)

// InspectFile 打开 path 并调用 Inspect。
func InspectFile(path string) (Info, error) {
	f, err := os.Open(path)
	if err != nil {
		return Info{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return Info{}, err
	}
	return Inspect(f, st.Size())
}

// Inspect 解析 r 中大小为 size 的 APK。清单问题返回 ErrNotAPK，签名问题返回
// ErrUnsigned（均可用 errors.Is 判断）；清单先于签名检查。
func Inspect(r io.ReaderAt, size int64) (Info, error) {
	if size <= 0 {
		return Info{}, fmt.Errorf("%w: empty file", ErrNotAPK)
	}
	manifest, err := readManifest(r, size)
	if err != nil {
		return Info{}, err
	}
	info, err := parseManifest(manifest)
	if err != nil {
		return Info{}, err
	}
	cert, scheme, err := readSigner(r, size)
	if err != nil {
		return Info{}, err
	}
	info.SignerCertSHA256 = cert
	info.SigningScheme = scheme
	return info, nil
}

// readManifest 从 zip 中读出 AndroidManifest.xml 的解压内容。
func readManifest(r io.ReaderAt, size int64) ([]byte, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil && !errors.Is(err, zip.ErrInsecurePath) {
		return nil, fmt.Errorf("%w: not a zip archive: %v", ErrNotAPK, err)
	}
	if zr == nil {
		return nil, fmt.Errorf("%w: not a zip archive", ErrNotAPK)
	}
	var mf *zip.File
	for _, f := range zr.File {
		if f.Name == "AndroidManifest.xml" {
			mf = f
			break
		}
	}
	if mf == nil {
		return nil, fmt.Errorf("%w: no AndroidManifest.xml", ErrNotAPK)
	}
	if mf.UncompressedSize64 > maxManifestSize {
		return nil, fmt.Errorf("%w: AndroidManifest.xml is %d bytes, limit %d", ErrNotAPK, mf.UncompressedSize64, maxManifestSize)
	}
	rc, err := mf.Open()
	if err != nil {
		return nil, fmt.Errorf("%w: open AndroidManifest.xml: %v", ErrNotAPK, err)
	}
	defer rc.Close()
	// 声明的解压大小不可信，按上限再截一次。
	data, err := io.ReadAll(io.LimitReader(rc, maxManifestSize+1))
	if err != nil {
		return nil, fmt.Errorf("%w: read AndroidManifest.xml: %v", ErrNotAPK, err)
	}
	if len(data) > maxManifestSize {
		return nil, fmt.Errorf("%w: AndroidManifest.xml exceeds %d bytes", ErrNotAPK, maxManifestSize)
	}
	return data, nil
}

// readFull 从 r 的 off 处读满 len(buf) 字节；ReaderAt 读满时附带的 io.EOF 不算错误。
func readFull(r io.ReaderAt, buf []byte, off int64) error {
	n, err := r.ReadAt(buf, off)
	if n == len(buf) {
		return nil
	}
	if err == nil || err == io.EOF {
		err = io.ErrUnexpectedEOF
	}
	return err
}
