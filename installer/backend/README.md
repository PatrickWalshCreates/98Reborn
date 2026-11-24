# installer/backend

`w9xm`: the patch-module engine (Go, no dependencies). It plans and applies modules to a mounted
Windows 98 SE drive, with dry-run diffs, backups, and rollback. The same binary will power the bootable
installer and the desktop ISO builder.

- Format and ops: [docs/MODULES.md](../../docs/MODULES.md)
- Test: `go test ./...`
- Build for every host OS: `./scripts/build-all.sh` (writes to `dist/`)

```
cmd/w9xm            CLI
internal/module     manifest, validation, ordering, conditions, params, templates, editions
internal/engine     overlay (copy-on-write view), ops, transactions/journal/rollback, diffs
internal/delta      W9XD delta make/apply + BSDIFF40 apply
internal/ini        Win9x INI editing (SYSTEM.INI, MSDOS.SYS)
internal/lines      CONFIG.SYS / AUTOEXEC.BAT marked blocks
internal/reg        REGEDIT4 + setupx INF generation
internal/textfile   CRLF / ^Z-preserving text handling
internal/iso9660    ISO 9660 + Joliet reader (no mounting)
internal/cab        Microsoft CAB reader: multi-cabinet sets, stored/MSZIP/LZX
internal/media      opens a 98 SE / Me CD image, detects the release, indexes every cabinet file
internal/pe         PE checksum verification
internal/fat        FAT12/16/32 read-write with long names, FAT32 format, MBR (no mounting, any host)
internal/lab        QEMU harness: unattended 98 SE install + in-guest module verification
cmd/vmprobe         debug aid: boot images in QEMU and take timed screenshots
```

`--target` accepts a mounted folder or a raw disk image. See [docs/LAB.md](../../docs/LAB.md) for the QEMU lab.

Tests in `internal/media` run against your own CD images in `<repo>/ISOs/` (git-ignored) and skip when they
aren't there. `go test -short ./...` skips the full-disc extraction sweep.
