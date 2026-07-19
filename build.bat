@echo off
REM Windows 开发构建: 先前端, 再 Go(嵌入前端产物)
cd web
call npm install
call npm run build
cd ..
go build -o opscore.exe .
echo Build done. Run: opscore.exe
