@echo off
setlocal enabledelayedexpansion

:: ==============================================================================
:: M365-Copilot2API - Windows to WSL Build Wrapper
:: Double-click or run from CMD/PowerShell to build inside WSL
:: ==============================================================================

cd /d "%~dp0"

echo ========================================================
echo  M365-Copilot2API - WSL Build Wrapper
echo ========================================================

:: Check if WSL is available
where wsl >nul 2>&1
if %errorlevel% neq 0 (
    echo [ERROR] WSL (Windows Subsystem for Linux) is not found in PATH.
    echo Please install or enable WSL first.
    pause
    exit /b 1
)

:: Execute build.sh inside WSL in the current project directory
wsl bash -c "cd \"$(wslpath '%~dp0')\" && chmod +x build.sh && ./build.sh %*"

if %errorlevel% neq 0 (
    echo.
    echo [ERROR] WSL build failed with exit code %errorlevel%.
    pause
    exit /b %errorlevel%
)

echo.
echo [DONE] Build completed successfully via WSL.
endlocal
