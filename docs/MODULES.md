# Patch Modules

A **module** is one self-contained fix or tweak for an installed Windows 98 SE system: a RAM cap, a driver, a
patched VxD, a registry setting. **Editions** are lists of modules. The `w9xm` engine
(`installer/backend`) applies modules to the mounted C: drive as a transaction: dry-run diff, backup, write,
journal, rollback.

`w9xm` is pure Go with no dependencies. It runs on Windows, macOS (Apple Silicon and Intel) and Linux, so the
same tool powers the bootable installer, a desktop ISO builder, and module development on your Mac.

## Layout

```
modules/<id>/module.json     manifest (id must equal the directory name)
modules/<id>/<payload files> our own files, or deltas: never Microsoft originals
editions/<id>/edition.json   {"id", "name", "description", "modules": [...], "params": {"module.param": value}}
```

## Manifest

```json
{
  "id": "ram-cap",
  "name": "Cap usable RAM",
  "version": "1.0.0",
  "description": "...",
  "category": "stability",
  "license": "MIT",
  "requires": [], "conflicts": [], "after": [],
  "when": { "ram_mb_min": 1025, "if_unknown": "apply" },
  "params": { "ram_cap_mb": { "type": "int", "default": 1024, "min": 256, "max": 1024 } },
  "ops": [
    { "op": "ini.set", "file": "{{windir}}\\SYSTEM.INI", "section": "386Enh",
      "key": "MaxPhysPage", "value": "{{ram_cap_mb|pages_hex}}" }
  ]
}
```

- `license`: the licence of the module's **payload**. Required, so the licence matrix stays honest.
- `requires`: pulled in automatically and ordered first. If a required module is skipped, so is this one.
  `after` only orders (it doesn't pull in). `conflicts` blocks co-selection.
- Unknown JSON fields are rejected, so typos fail loudly.

### Conditions (`when`)
All present criteria must match the facts from the installer's hardware scan (`--facts`):
`pci_any` (`"8086:2922"`, `"8086:*"`), `pci_class_any` (prefix, e.g. `"0106"` = AHCI), `ram_mb_min`,
`ram_mb_max`, `cpu_mhz_min`, `facts` (equality on `extra`). When a fact is missing, `if_unknown` decides:
`skip` (default) or `apply` (for protective fixes that are harmless when not needed).

Facts file:
```json
{ "ram_mb": 16384, "cpu_mhz": 4200,
  "pci": [{ "id": "8086:a282", "class": "0106", "name": "SATA AHCI" }],
  "extra": { "sata_mode": "ahci" } }
```

### Parameters and templates
Params have `type` `int` (`min`/`max`), `bool` (rendered `1`/`0`) or `string` (`enum`). Values come from the
default, then the edition's `params`, then `--set module.param=value`.

String fields accept `{{name}}` or `{{name|filter}}`. `name` can be a param, `windir`, or `facts.ram_mb` /
`facts.cpu_mhz` / `facts.<extra>`.

| Filter | Converts | Example |
|---|---|---|
| `pages_hex` | MB to 4 KB pages, in hex | `1024` → `40000` (MaxPhysPage) |
| `kb` | MB to KB | `256` → `262144` (MaxFileCache) |
| `hex` | integer to hex | |
| `upper` / `lower` | changes case | |

## Ops

Paths are Windows-style, relative to the drive root (`C:\` optional), and resolved case-insensitively.
`W9XM\TX` is reserved for the journal.

| op | Fields | Behaviour |
|---|---|---|
| `file.copy` | `file`, `src`, `sha256`?, `overwrite`? | Install a payload file. Same content is a no-op. |
| `file.delete` | `file` | Remove a file. It is backed up. |
| `file.delta` | `file`, `delta`, (`sha256_before`, `sha256_after` for bsdiff) | Patch a file in place. **W9XD** deltas carry source and result hashes. Classic **BSDIFF40** is also accepted. |
| `file.bytes` | `file`, `patches: [{offset, old, new}]`, `sha256_before`?, `sha256_after`? | Hex byte patch with verification, for documented community hex patches. |
| `ini.set` | `file`, `section`, `key`, `value` | Single value. Updates the first occurrence, removes duplicates, appends if missing. |
| `ini.add` | same | Adds the pair unless it already exists. For multi-value keys (`device=`). |
| `ini.delete` | `file`, `section`, `key`, `value`? | Removes the key, or only the matching pair if `value` is given. |
| `lines.block` | `file`, `block`, `lines`, `position` (`start`/`end`) | Idempotent `REM W9XM BEGIN/END <block>` section in CONFIG.SYS or AUTOEXEC.BAT. |
| `lines.remove_block` | `file`, `block` | |
| `lines.edit` | `file`, `match` (regex, case-insensitive), `action` (`comment`/`delete`) | Skips comments and W9XM blocks. |
| `reg.set` | `path` (`HKLM\...`), `name` (empty = default), `type` (`sz`/`dword`/`binary`), `data` | See *Registry*. |
| `reg.delete` | `path`, `name`? (omit = whole key) | See *Registry*. |
| `media.extract` | `source` (file name inside the CD's setup cabinets), `file`, `media` (`98se` default, `me`, `98`, `any`), `sha256`?, `overwrite`? | Copies an original file from the user's own CD into the target. See *Original media*. |

Any op can set `"optional": true`: if the target isn't in the expected state (wrong file version, file
missing), the op is skipped with a note instead of failing the whole plan.

**Safety:** a non-optional mismatch aborts the plan before anything is written. Apply re-checks every file
against the plan and refuses if anything changed on disk in between.

Text edits preserve CRLF line endings, a missing final newline, a trailing `^Z`, and the formatting of
untouched lines. They also keep file attributes. MSDOS.SYS is read-only; the engine lifts that temporarily
and restores it.

### Registry
Win98's `SYSTEM.DAT`/`USER.DAT` can't be edited offline with NT tools, so the engine queues changes:
- **Sets** go to `C:\W9XM\PENDING.REG` (REGEDIT4). A `REM W9XM BEGIN registry` block at the top of
  AUTOEXEC.BAT imports it with **real-mode REGEDIT** on the next boot, before Windows loads, then renames it
  to `APPLIED.REG`.
- **Deletes** go to a setupx INF (`C:\W9XM\Dxxxxxxx.INF`, DelReg section). It runs once at first boot via
  `RunOnce`, because Win98's REGEDIT4 importer has no delete syntax.

> **Verified in the QEMU lab** (see [LAB.md](LAB.md)): sets, including `HKCU` via `/R:USER.DAT` on a
> single-user system, and deletes via RunOnce/setupx both take effect in a real 98 SE install. Still to do
> on real hardware in Phase 0.

## Original media

`--media ISO` (repeatable) gives the engine the user's own CD images. The engine reads them directly (ISO 9660
+ Joliet, then multi-cabinet LZX/MSZIP CABs), so nothing needs mounting and no third-party tools are needed on
any host OS. `media.extract` pulls a file out of the setup cabinets (`WIN98\` on 98 SE, `WIN9X\` on Me).

This is how modules use Microsoft files without the repo shipping any:
- *Restore or replace from the original:* e.g. re-extract a pristine file before applying a delta.
- *Backport from Me:* e.g. Me's USB mass-storage stack (`USBSTOR.SYS`, `USBHUB.SYS`, ...) onto 98 SE,
  the way 98SE2ME-style projects do, but taken from the user's own Me disc.

Pin `sha256` so a module only uses the exact release it was tested with. An optional op is skipped (with a
note) when the CD isn't provided. A required one fails the plan and tells the user to pass `--media`.

```bash
w9xm media info ISOs/98se.iso                      # product, release fingerprint, cabinet summary
w9xm media ls ISOs/me.iso 'usb*'                   # find files across all setup cabinets
w9xm media extract ISOs/98se.iso --out x '*.vxd'   # extract (add --all for add-on cabinets)
w9xm iso ls ISOs/98se.iso win98                    # browse the disc itself
w9xm cab ls a.cab b.cab                            # standalone cabinets (pass every part of a set)
```

Verified against the 98 SE and Me CDs: every setup file extracts (about 340 MB and 390 MB respectively),
2,700+ PE files match their stored checksums, and cabinet copies match the loose copies on the disc byte for
byte. Quantum-compressed cabinets (only some add-ons, such as `ado.cab`) aren't supported yet. They're
reported, and everything else still extracts.

## Transactions

Each `apply` creates `C:\W9XM\TX\<id>\journal.json` plus `NNNN.BAK` backups of every file it changes.
- `w9xm status` lists transactions and applied modules. Modules already applied at the same version are
  skipped unless you pass `--reapply`.
- `w9xm rollback [--tx ID]` restores the files and removes created files and directories. It refuses if a
  newer transaction touched the same files, or if the files were changed since. Pass `--force` to override.
- If a write fails mid-apply, the files already written are restored automatically.

The Control Center inside Windows 98 will read the same journals to show build info and offer rollback.

## Authoring a delta (legally)

1. Keep the original Microsoft file **outside the repo** (`.gitignore` blocks `*.iso`, `*.cab`, `user/`).
2. Produce your fixed version: hex edit, or a clean-room rebuild.
3. Run `w9xm delta make ORIGINAL PATCHED modules/<id>/<name>.w9xd`. It prints the before and after hashes.
4. Reference it with a `file.delta` op. The delta only works against the exact original file, so it can't
   reconstruct the Microsoft file on its own.

## CLI

```bash
w9xm modules validate --modules modules --editions editions
w9xm plan  --target /Volumes/WIN98 --edition gaming --facts facts.json --set ram-cap.ram_cap_mb=768
w9xm apply --target /Volumes/WIN98 --edition gaming --facts facts.json
w9xm apply --target lab/runs/x/disk.img --edition core     # a raw disk image works too: no mounting
w9xm status --target /Volumes/WIN98
w9xm rollback --target /Volumes/WIN98
```

To build for every host OS, run `installer/backend/scripts/build-all.sh`. It writes to `dist/`:
darwin arm64/amd64, linux amd64/arm64/386, windows amd64/386/arm64.

## Not built yet
- Quantum decompression, for a few add-on cabinets only.
- Signed module bundles (ed25519) for downloads from the update server.
- A test suite that boots in QEMU and checks that each module works inside a real 98 SE install.
