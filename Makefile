# OpsCore demo 构建脚本
# Linux / macOS:  make build && ./opscore
# Windows (Git Bash): make build && ./opscore.exe

web:
	cd web && npm install && npm run build

build: web
	go build -o opscore .

run: build
	./opscore

clean:
	rm -rf web/dist opscore opscore.exe

.PHONY: web build run clean
