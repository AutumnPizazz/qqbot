@echo off
rem sshbox 构建入口（Windows）：自动读取 server-info.txt 注入凭据并产出 sshbox.exe
rem 用法: build.bat [可选的 -Conf 路径] [-Out 输出路径] [-NoStrip]
powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0build.ps1" %*
