$ErrorActionPreference='Continue'
$d = Join-Path $env:TEMP "gorepl"; if (Test-Path $d) { Remove-Item $d -Recurse -Force }
New-Item -ItemType Directory -Path $d | Out-Null
Copy-Item C:/Users/Public/mcp-A.exe (Join-Path $d "srv.exe")
$target = Join-Path $d "srv.exe"

Write-Output ("1. identity before      : " + (& $target -version))
$p = Start-Process -FilePath $target -ArgumentList "-resident" -PassThru -NoNewWindow -RedirectStandardOutput (Join-Path $d "res.txt")
Start-Sleep -Seconds 3
if ($p.HasExited) { Write-Output ("   ПОМИЛКА ТЕСТУ: резидентний процес помер, exit=" + $p.ExitCode + " - результат недійсний"); exit 1 }
Write-Output "   (резидентний процес живий - файл справді утримується)"
Write-Output ("2. resident process says: " + ((Get-Content (Join-Path $d "res.txt") -Raw).Trim()) + "  (pid " + $p.Id + ")")

Write-Output "3. overwriting the file WHILE that process is running..."
try {
  Copy-Item C:/Users/Public/mcp-B.exe $target -Force -ErrorAction Stop
  Write-Output "   VERDICT: overwrite PERMITTED"
} catch {
  Write-Output ("   VERDICT: overwrite REFUSED -> " + $_.Exception.GetType().Name + ": " + $_.Exception.Message)
}
Write-Output "3b. trying rename-then-place (the Chrome pattern)..."
try {
  $old = Join-Path $d "srv.old.exe"
  Move-Item $target $old -Force -ErrorAction Stop
  Copy-Item C:/Users/Public/mcp-B.exe $target -Force -ErrorAction Stop
  Write-Output "   VERDICT: rename+place PERMITTED"
} catch { Write-Output ("   VERDICT: rename+place REFUSED -> " + $_.Exception.Message) }

Write-Output ("4. resident process still says: " + ((Get-Content (Join-Path $d "res.txt") -Raw).Trim()))
Write-Output ("5. NEW launch from same path  : " + (& $target -version))
Stop-Process -Id $p.Id -Force -ErrorAction SilentlyContinue
