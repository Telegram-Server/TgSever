# 重新生成 internal/apkinfo/testdata 下的测试 APK。在仓库根目录执行：
#   pwsh internal/apkinfo/testdata/gen.ps1
# 密钥库已提交，存在时不会重新生成（重新生成会改变证书指纹，测试里的期望值也要跟着改）。
$ErrorActionPreference = 'Stop'
$sdk = "$env:LOCALAPPDATA\Android\Sdk"
$bt = "$sdk\build-tools\36.1.0"
$androidJar = "$sdk\platforms\android-36\android.jar"
$td = Join-Path $PSScriptRoot '.'
$tmp = Join-Path ([System.IO.Path]::GetTempPath()) "apkinfo-gen"
New-Item -ItemType Directory -Force $tmp | Out-Null
$pass = 'apkinfo-test'

function Invoke-Tool {
    & $args[0] @($args | Select-Object -Skip 1)
    if ($LASTEXITCODE -ne 0) { throw "失败: $($args -join ' ')" }
}

foreach ($k in 'a', 'b') {
    $ks = Join-Path $td "key-$k.p12"
    if (-not (Test-Path $ks)) {
        Invoke-Tool keytool -genkeypair -keystore $ks -storetype PKCS12 -storepass $pass -keypass $pass `
            -alias $k -keyalg EC -groupname secp256r1 -validity 36500 -dname "CN=apkinfo test key $k"
    }
}

# 1) aapt2 link：只含 AndroidManifest.xml 的未签名 APK，再 zipalign。
function New-Unsigned($manifest, $out) {
    $raw = Join-Path $tmp "$out.raw.apk"
    Invoke-Tool "$bt\aapt2.exe" link -o $raw -I $androidJar --manifest (Join-Path $td "src\$manifest")
    $aligned = Join-Path $tmp "$out.aligned.apk"
    Invoke-Tool "$bt\zipalign.exe" -f -p 4 $raw $aligned
    return $aligned
}

# 2) apksigner 签名。
function New-Signed($in, $out, $key, [string[]]$schemes) {
    $dst = Join-Path $td $out
    $flags = @('--v1-signing-enabled', 'false', '--v2-signing-enabled', 'false', '--v3-signing-enabled', 'false', '--v4-signing-enabled', 'false')
    foreach ($s in $schemes) { $flags[([array]::IndexOf($flags, "--$s-signing-enabled")) + 1] = 'true' }
    Invoke-Tool "$bt\apksigner.bat" sign --ks (Join-Path $td "key-$key.p12") --ks-pass "pass:$pass" --ks-key-alias $key `
        @flags --out $dst $in
    Remove-Item -ErrorAction SilentlyContinue "$dst.idsig"
}

$u1234 = New-Unsigned 'v1234.xml' 'v1234'
$u1300 = New-Unsigned 'v1300.xml' 'v1300'
$umajor = New-Unsigned 'major.xml' 'major'

New-Signed $u1234 'a-v1234-v2v3.apk' 'a' @('v2', 'v3')
New-Signed $u1300 'a-v1300-v2v3.apk' 'a' @('v2', 'v3')
New-Signed $u1234 'b-v1234-v2v3.apk' 'b' @('v2', 'v3')
New-Signed $u1234 'a-v1234-v2only.apk' 'a' @('v2')
New-Signed $u1234 'a-v1234-v1only.apk' 'a' @('v1')
New-Signed $umajor 'a-major-v3only.apk' 'a' @('v3')
Copy-Item -Force $u1234 (Join-Path $td 'unsigned-v1234.apk')

foreach ($f in Get-ChildItem $td -Filter *.apk) {
    Write-Output "== $($f.Name) ($($f.Length) bytes)"
    & "$bt\apksigner.bat" verify --print-certs -v $f.FullName 2>&1 | Select-String -Pattern 'Verified using|SHA-256|DOES NOT VERIFY|ERROR'
}
