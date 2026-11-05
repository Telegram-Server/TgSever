package apkinfo

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
)

// 本文件从 APK Signing Block 读出第一个签名者的第一张证书，格式见
// https://source.android.com/docs/security/features/apksigning/v2 与 v3。整数均为小端。
//
//	[zip 条目] | APK Signing Block | 中央目录 | EOCD
//
//	APK Signing Block:
//	  u64 size（不含本字段）
//	  若干 ID-value 对：u64 len | u32 id | value（len-4 字节）
//	  u64 size（与开头相同）
//	  "APK Sig Block 42"
//
//	v2/v3 value: lp(signers)，signers = lp(signer)*
//	  signer: lp(signed data) | ...（v3 在其后还有 minSDK/maxSDK）
//	  signed data: lp(digests) | lp(certificates) | ...
//	  certificates = lp(X.509 DER)*
//
// lp(x) 表示 u32 长度前缀的 x。只读结构，不校验摘要和签名。

const (
	eocdSignature    = 0x06054b50
	eocdMinSize      = 22
	maxZipComment    = 0xffff
	sigBlockMagic    = "APK Sig Block 42"
	sigBlockFooter   = 8 + len(sigBlockMagic) // u64 size + magic
	sigBlockMinSize  = 8 + sigBlockFooter     // 首部 size + 尾部，没有任何 ID-value 对
	blockIDV2        = 0x7109871a
	blockIDV3        = 0xf05368c0
	zip64OffsetValue = 0xffffffff
)

// readSigner 返回签名证书的 SHA-256（小写十六进制）与签名方案（3 或 2）。
func readSigner(r io.ReaderAt, size int64) (string, int, error) {
	fail := func(format string, args ...any) (string, int, error) {
		return "", 0, fmt.Errorf("%w: "+format, append([]any{ErrUnsigned}, args...)...)
	}
	cdOffset, err := centralDirectoryOffset(r, size)
	if err != nil {
		return fail("%v", err)
	}
	block, err := readSigningBlock(r, cdOffset)
	if err != nil {
		return fail("%v", err)
	}
	pairs := block[8 : len(block)-sigBlockFooter]
	var v2, v3 []byte
	for len(pairs) > 0 {
		if len(pairs) < 8 {
			return fail("signing block: truncated pair length")
		}
		n := binary.LittleEndian.Uint64(pairs)
		pairs = pairs[8:]
		if n < 4 || n > uint64(len(pairs)) {
			return fail("signing block: pair length %d out of range", n)
		}
		id := binary.LittleEndian.Uint32(pairs)
		val := pairs[4:n]
		pairs = pairs[n:]
		switch id {
		case blockIDV3:
			if v3 == nil {
				v3 = val
			}
		case blockIDV2:
			if v2 == nil {
				v2 = val
			}
		}
	}
	for _, s := range []struct {
		scheme int
		val    []byte
	}{{3, v3}, {2, v2}} {
		if s.val == nil {
			continue
		}
		cert, err := firstCertificate(s.val)
		if err != nil {
			return fail("v%d signature: %v", s.scheme, err)
		}
		if cert == nil {
			continue // 该方案没有签名者，试下一个。
		}
		sum := sha256.Sum256(cert)
		return hex.EncodeToString(sum[:]), s.scheme, nil
	}
	return fail("no v3 or v2 signer in APK Signing Block")
}

// centralDirectoryOffset 找到 EOCD 并返回中央目录偏移。
func centralDirectoryOffset(r io.ReaderAt, size int64) (int64, error) {
	if size < eocdMinSize {
		return 0, fmt.Errorf("file too small for zip EOCD")
	}
	tailLen := int64(eocdMinSize + maxZipComment)
	if tailLen > size {
		tailLen = size
	}
	tail := make([]byte, tailLen)
	if err := readFull(r, tail, size-tailLen); err != nil {
		return 0, fmt.Errorf("read zip tail: %v", err)
	}
	// 从后往前找签名，且要求注释长度字段恰好覆盖到文件尾，避免注释里出现的假签名。
	for i := len(tail) - eocdMinSize; i >= 0; i-- {
		if binary.LittleEndian.Uint32(tail[i:]) != eocdSignature {
			continue
		}
		commentLen := int(binary.LittleEndian.Uint16(tail[i+20:]))
		if i+eocdMinSize+commentLen != len(tail) {
			continue
		}
		cd := binary.LittleEndian.Uint32(tail[i+16:])
		if cd == zip64OffsetValue {
			return 0, fmt.Errorf("zip64 archives are not supported")
		}
		eocdPos := size - tailLen + int64(i)
		if int64(cd) > eocdPos {
			return 0, fmt.Errorf("central directory offset %d beyond EOCD at %d", cd, eocdPos)
		}
		return int64(cd), nil
	}
	return 0, fmt.Errorf("zip EOCD not found")
}

// readSigningBlock 读出紧挨在中央目录之前的整个 APK Signing Block（含首尾 size 字段与魔数）。
func readSigningBlock(r io.ReaderAt, cdOffset int64) ([]byte, error) {
	if cdOffset < int64(sigBlockMinSize) {
		return nil, fmt.Errorf("no APK Signing Block")
	}
	footer := make([]byte, sigBlockFooter)
	if err := readFull(r, footer, cdOffset-int64(sigBlockFooter)); err != nil {
		return nil, fmt.Errorf("read signing block footer: %v", err)
	}
	if !bytes.Equal(footer[8:], []byte(sigBlockMagic)) {
		return nil, fmt.Errorf("no APK Signing Block")
	}
	n := binary.LittleEndian.Uint64(footer)
	// n 不含开头的 u64 size 字段本身。
	if n < uint64(sigBlockFooter) || n > maxSigningBlockSize-8 || n+8 > uint64(cdOffset) {
		return nil, fmt.Errorf("signing block size %d out of range", n)
	}
	block := make([]byte, n+8)
	start := cdOffset - int64(n+8)
	if err := readFull(r, block, start); err != nil {
		return nil, fmt.Errorf("read signing block: %v", err)
	}
	if binary.LittleEndian.Uint64(block) != n {
		return nil, fmt.Errorf("signing block header size does not match footer")
	}
	return block, nil
}

// lengthPrefixed 切出 b 开头的 u32 长度前缀字段，返回字段内容与其后的剩余字节。
func lengthPrefixed(b []byte) ([]byte, []byte, error) {
	if len(b) < 4 {
		return nil, nil, fmt.Errorf("truncated length prefix")
	}
	n := uint64(binary.LittleEndian.Uint32(b))
	b = b[4:]
	if n > uint64(len(b)) {
		return nil, nil, fmt.Errorf("length %d exceeds remaining %d bytes", n, len(b))
	}
	return b[:n], b[n:], nil
}

// firstCertificate 返回 v2/v3 value 里第一个签名者的第一张证书；没有签名者时返回 nil, nil。
func firstCertificate(val []byte) ([]byte, error) {
	signers, _, err := lengthPrefixed(val)
	if err != nil {
		return nil, fmt.Errorf("signers: %v", err)
	}
	if len(signers) == 0 {
		return nil, nil
	}
	signer, _, err := lengthPrefixed(signers)
	if err != nil {
		return nil, fmt.Errorf("signer: %v", err)
	}
	signedData, _, err := lengthPrefixed(signer)
	if err != nil {
		return nil, fmt.Errorf("signed data: %v", err)
	}
	_, rest, err := lengthPrefixed(signedData) // digests
	if err != nil {
		return nil, fmt.Errorf("digests: %v", err)
	}
	certs, _, err := lengthPrefixed(rest)
	if err != nil {
		return nil, fmt.Errorf("certificates: %v", err)
	}
	cert, _, err := lengthPrefixed(certs)
	if err != nil {
		return nil, fmt.Errorf("first certificate: %v", err)
	}
	if len(cert) == 0 {
		return nil, fmt.Errorf("empty certificate")
	}
	return cert, nil
}
