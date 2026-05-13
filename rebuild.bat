@echo off
echo ================================
echo  NAWALA BOT - Rebuild
echo ================================

:: Stop proses lama kalau masih jalan
taskkill /f /im nawala-bot.exe >nul 2>&1

:: Build ulang
echo [1/2] Building...
go build -o nawala-bot.exe .

if %errorlevel% neq 0 (
    echo.
    echo [ERROR] Build gagal! Cek error di atas.
    pause
    exit /b 1
)

echo [2/2] Build sukses!
echo.
echo Jalankan bot dengan: nawala-bot.exe
echo ================================
pause
