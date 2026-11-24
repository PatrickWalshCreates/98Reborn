# Win9x-Modern (working name)

A modern, network-aware installer that puts a patched, driver-equipped **Windows 98 SE** onto current PCs,
plus a built-in **Control Center** for specs, monitoring and updates. Core and Gaming editions.

**Bring your own Windows:** this project ships no Microsoft code. You supply your own 98 SE CD/ISO and key;
the installer patches it locally.

- Plan & architecture: [docs/PLAN.md](docs/PLAN.md)

| Path | What |
|---|---|
| `installer/` | Linux-based live installer: live env, Go backend, web UI |
| `modules/` | Patch/fix modules (deltas, ini/registry edits) |
| `drivers/` | Signed driver manifest keyed by PCI/USB ID |
| `control-center/` | Win32 management app that runs inside 98 SE |
| `editions/` | Core and Gaming module lists |
| `site/` | GitHub Pages website |
| `tests/` | QEMU/86Box install tests, hardware compatibility reports |
