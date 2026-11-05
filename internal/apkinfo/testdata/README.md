# apkinfo 测试样本

本目录下的 APK 是用 Android SDK 官方工具生成的真实小包，只含一个 `AndroidManifest.xml`
（`android:hasCode="false"`，没有 dex）和 aapt2 生成的 `resources.arsc`。测试里硬编码的证书
指纹取自 `apksigner verify --print-certs`，用官方工具交叉验证 `apkinfo` 的解析结果。

`key-a.p12`、`key-b.p12` 是**只用于测试**的 EC P-256 密钥库（口令 `apkinfo-test`），随样本一起
提交。不要用它们签任何真实发布的包。

## 样本

| 文件 | 清单源 | 密钥 | 签名方案 | 期望 |
| --- | --- | --- | --- | --- |
| `a-v1234-v2v3.apk` | `src/v1234.xml` | A | v2 + v3 | cc.example.app / 1234 / 12.2.10 / minSdk 24，v3，证书 A |
| `a-v1300-v2v3.apk` | `src/v1300.xml` | A | v2 + v3 | cc.example.app / 1300 / 12.3.0 / minSdk 24，v3，证书 A |
| `b-v1234-v2v3.apk` | `src/v1234.xml` | B | v2 + v3 | 同第一行，证书 B |
| `a-v1234-v2only.apk` | `src/v1234.xml` | A | 仅 v2 | 同第一行，v2，证书 A |
| `a-major-v3only.apk` | `src/major.xml` | A | 仅 v3 | versionCodeMajor=2、versionCode=7 → 2<<32\|7 / 20.0.7 / minSdk 28，v3，证书 A |
| `a-v1234-v1only.apk` | `src/v1234.xml` | A | 仅 v1（JAR） | `ErrUnsigned` |
| `unsigned-v1234.apk` | `src/v1234.xml` | — | 无 | `ErrUnsigned` |

证书 SHA-256：

- 密钥 A：`036c7da6ef8e90cb5731f0d9270ff204b33347b840425e24e9b7d34a30076b2e`
- 密钥 B：`1f1421ffbd6984e9faae9e6074e188fb221f21eda378c3a06584e699cc1c1a91`

`apksigner verify` 对 v1-only 样本会报 “Target SDK version 36 requires a minimum of signature
scheme v2” 而拒绝打印证书；它的 v1 证书可用 `keytool -printcert -jarfile a-v1234-v1only.apk`
核对，同样是密钥 A 的指纹。

## 重新生成

需要 Android SDK（build-tools 36.1.0、platforms/android-36）和 PATH 里的 `keytool`。在仓库根目录：

```powershell
pwsh internal/apkinfo/testdata/gen.ps1
```

`gen.ps1` 做的事情（等价的完整命令，`$td` 为本目录）：

```powershell
$sdk = "$env:LOCALAPPDATA\Android\Sdk"
$bt  = "$sdk\build-tools\36.1.0"
$jar = "$sdk\platforms\android-36\android.jar"
$td  = "internal\apkinfo\testdata"

# 1. 测试密钥（已提交；存在时跳过，重新生成会改变指纹，测试期望值也要跟着改）
keytool -genkeypair -keystore $td\key-a.p12 -storetype PKCS12 -storepass apkinfo-test -keypass apkinfo-test `
  -alias a -keyalg EC -groupname secp256r1 -validity 36500 -dname "CN=apkinfo test key a"
keytool -genkeypair -keystore $td\key-b.p12 -storetype PKCS12 -storepass apkinfo-test -keypass apkinfo-test `
  -alias b -keyalg EC -groupname secp256r1 -validity 36500 -dname "CN=apkinfo test key b"

# 2. 编译清单为未签名 APK 并 4 字节对齐（v1300.xml、major.xml 同理）
& $bt\aapt2.exe link -o $env:TEMP\v1234.raw.apk -I $jar --manifest $td\src\v1234.xml
& $bt\zipalign.exe -f -p 4 $env:TEMP\v1234.raw.apk $env:TEMP\v1234.aligned.apk

# 3. 签名（每个样本只开所需的方案，其余显式关闭；v4 一律关闭，避免产生 .idsig）
& $bt\apksigner.bat sign --ks $td\key-a.p12 --ks-pass pass:apkinfo-test --ks-key-alias a `
  --v1-signing-enabled false --v2-signing-enabled true --v3-signing-enabled true --v4-signing-enabled false `
  --out $td\a-v1234-v2v3.apk $env:TEMP\v1234.aligned.apk
#   a-v1300-v2v3.apk   ：v1300 输入，同上
#   b-v1234-v2v3.apk   ：--ks $td\key-b.p12 --ks-key-alias b，同上
#   a-v1234-v2only.apk ：--v2-signing-enabled true  --v3-signing-enabled false
#   a-major-v3only.apk ：major 输入，--v2-signing-enabled false --v3-signing-enabled true
#   a-v1234-v1only.apk ：--v1-signing-enabled true  --v2-signing-enabled false --v3-signing-enabled false
#   unsigned-v1234.apk ：直接复制 v1234.aligned.apk

# 4. 读取期望指纹
& $bt\apksigner.bat verify --print-certs -v $td\a-v1234-v2v3.apk
```

注意：apksigner 签名带随机数（ECDSA），重新签名会改变 APK 字节，但证书与指纹不变，测试不受影响。

本目录下的 `.apk` / `.p12` 均未被 `.gitignore` 忽略（已用 `git check-ignore -v` 确认）。
