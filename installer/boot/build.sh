#!/bin/sh
# Builds 98Reborn Boot (BOOTX64.EFI) with clang and lld-link, on macOS or
# Linux. No EFI SDK needed: src/efi.h has the definitions it uses.
#   macOS: brew install llvm lld      Linux: apt install clang lld
set -eu
cd "$(dirname "$0")"
CC="${CC:-clang}"
LINK="${LINK:-lld-link}"
if ! command -v "$LINK" >/dev/null 2>&1; then
  for d in /opt/homebrew/opt/lld/bin /usr/local/opt/lld/bin /usr/lib/llvm-*/bin; do
    [ -x "$d/lld-link" ] && PATH="$d:$PATH" && break
  done
fi
mkdir -p out
$CC -target x86_64-unknown-windows -std=c11 -O2 -Wall -Wno-unused-function \
  -ffreestanding -fshort-wchar -mno-red-zone -fno-stack-protector -fno-stack-check \
  -mno-stack-arg-probe -c src/main.c -o out/main.o
$LINK -subsystem:efi_application -entry:efi_main -nodefaultlib \
  -out:out/98reborn-boot.efi out/main.o >/dev/null
echo "built out/98reborn-boot.efi ($(wc -c < out/98reborn-boot.efi) bytes)"
