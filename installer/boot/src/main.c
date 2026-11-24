/* 98Reborn Boot: the UEFI boot menu for PCs running Windows 98 with 98Reborn.
 *
 * It lists Windows 98 and the other systems it finds (Windows Boot Manager,
 * Linux boot loaders) on every disk, and starts the one chosen.
 *
 * Windows 98 needs a legacy BIOS. On UEFI-only PCs that comes from ReBIOS
 * (SeaBIOS under a minimal hypervisor; needs VT-x/AMD-V and an IOMMU) or, on
 * PCs without those, CSMWrap. Both are started from \EFI\98REBORN on the
 * Windows 98 partition, where this program lives too, and then boot that
 * same disk's MBR.
 *
 * Settings: \EFI\98REBORN\BOOT.CFG (key=value lines):
 *   timeout=5        seconds before the default starts (0 = immediately)
 *   default=win98    or the title of another entry
 *   engine=auto      auto | rebios | csmwrap
 */
#include "efi.h"
#include "font.h"

static EFI_SYSTEM_TABLE *ST;
static EFI_BOOT_SERVICES *BS;
static EFI_HANDLE Self;

static const EFI_GUID LoadedImageGuid = {0x5B1B31A1, 0x9562, 0x11d2, {0x8E, 0x3F, 0x00, 0xA0, 0xC9, 0x69, 0x72, 0x3B}};
static const EFI_GUID SimpleFsGuid = {0x964E5B22, 0x6459, 0x11d2, {0x8E, 0x39, 0x00, 0xA0, 0xC9, 0x69, 0x72, 0x3B}};
static const EFI_GUID GopGuid = {0x9042a9de, 0x23dc, 0x4a38, {0x96, 0xfb, 0x7a, 0xde, 0xd0, 0x80, 0x51, 0x6a}};
static const EFI_GUID DevicePathGuid = {0x09576e91, 0x6d3f, 0x11d2, {0x8e, 0x39, 0x00, 0xa0, 0xc9, 0x69, 0x72, 0x3b}};
static const EFI_GUID GlobalVarGuid = {0x8BE4DF61, 0x93CA, 0x11d2, {0xAA, 0x0D, 0x00, 0xE0, 0x98, 0x03, 0x2B, 0x8C}};

/* The compiler may emit calls to these. */
void *memset(void *d, int c, UINTN n) { u8 *p = d; while (n--) *p++ = (u8)c; return d; }
void *memcpy(void *d, const void *s, UINTN n) { u8 *a = d; const u8 *b = s; while (n--) *a++ = *b++; return d; }

/* ---------- small helpers ---------- */

static UINTN wlen(const CHAR16 *s) { UINTN n = 0; while (s[n]) n++; return n; }
static void wcopy(CHAR16 *d, const CHAR16 *s, UINTN max) { UINTN i = 0; for (; s[i] && i + 1 < max; i++) d[i] = s[i]; d[i] = 0; }
static void wcat(CHAR16 *d, const CHAR16 *s, UINTN max) { UINTN n = wlen(d); wcopy(d + n, s, max - n); }
static void a2w(CHAR16 *d, const char *s, UINTN max) { UINTN i = 0; for (; s[i] && i + 1 < max; i++) d[i] = (u8)s[i]; d[i] = 0; }
static CHAR16 lower(CHAR16 c) { return c >= 'A' && c <= 'Z' ? c + 32 : c; }
static int wieq(const CHAR16 *a, const CHAR16 *b) {
	while (*a && *b) if (lower(*a++) != lower(*b++)) return 0;
	return *a == *b;
}
static void utoa(CHAR16 *d, u32 v) {
	CHAR16 t[12]; int n = 0;
	do { t[n++] = '0' + v % 10; v /= 10; } while (v);
	while (n) *d++ = t[--n];
	*d = 0;
}
static void *alloc(UINTN n) { void *p = 0; if (EFI_ERROR(BS->AllocatePool(EfiLoaderData, n, &p))) return 0; return p; }

/* ---------- configuration ---------- */

static u32 cfg_timeout = 5;
static CHAR16 cfg_default[64] = u"win98";
static char cfg_engine[16] = "auto";

static EFI_FILE *open_root(EFI_HANDLE dev) {
	EFI_SIMPLE_FILE_SYSTEM *fs; EFI_FILE *root;
	if (EFI_ERROR(BS->HandleProtocol(dev, &SimpleFsGuid, (void **)&fs))) return 0;
	if (EFI_ERROR(fs->OpenVolume(fs, &root))) return 0;
	return root;
}

static int file_exists(EFI_FILE *root, const CHAR16 *path) {
	EFI_FILE *f;
	if (EFI_ERROR(root->Open(root, &f, path, EFI_FILE_MODE_READ, 0))) return 0;
	f->Close(f);
	return 1;
}

static void load_config(EFI_FILE *root) {
	EFI_FILE *f; char buf[1024]; UINTN n = sizeof buf - 1;
	if (!root || EFI_ERROR(root->Open(root, &f, u"\\EFI\\98REBORN\\BOOT.CFG", EFI_FILE_MODE_READ, 0))) return;
	if (EFI_ERROR(f->Read(f, &n, buf))) n = 0;
	f->Close(f);
	buf[n] = 0;
	char *p = buf;
	while (*p) {
		char *line = p;
		while (*p && *p != '\n') p++;
		if (*p) *p++ = 0;
		char *eq = line;
		while (*eq && *eq != '=') eq++;
		if (!*eq || *line == '#' || *line == ';') continue;
		*eq = 0;
		char *v = eq + 1;
		for (char *e = v; *e; e++) if (*e == '\r' || *e == ' ') { *e = 0; break; }
		if (line[0] == 't' && line[1] == 'i') { u32 t = 0; while (*v >= '0' && *v <= '9') t = t * 10 + (*v++ - '0'); cfg_timeout = t; }
		else if (line[0] == 'd') a2w(cfg_default, v, 64);
		else if (line[0] == 'e') { int i = 0; for (; v[i] && i < 15; i++) cfg_engine[i] = v[i]; cfg_engine[i] = 0; }
	}
}

/* ---------- boot entries ---------- */

enum kind { K_WIN98, K_EFI, K_FIRMWARE, K_POWEROFF };

struct entry {
	enum kind kind;
	CHAR16 title[64];
	CHAR16 detail[64];
	EFI_HANDLE dev;
	CHAR16 path[128];
};

static struct entry entries[16];
static int nentries;

static struct entry *add(enum kind k, const CHAR16 *title, const CHAR16 *detail) {
	if (nentries >= 16) return 0;
	struct entry *e = &entries[nentries++];
	memset(e, 0, sizeof *e);
	e->kind = k;
	wcopy(e->title, title, 64);
	wcopy(e->detail, detail, 64);
	return e;
}

/* Linux distributions keep their loader in \EFI\<name>\. */
static void scan_linux(EFI_FILE *root, EFI_HANDLE dev) {
	EFI_FILE *dir;
	if (EFI_ERROR(root->Open(root, &dir, u"\\EFI", EFI_FILE_MODE_READ, 0))) return;
	u8 buf[512];
	for (;;) {
		UINTN n = sizeof buf;
		if (EFI_ERROR(dir->Read(dir, &n, buf)) || n == 0) break;
		EFI_FILE_INFO *fi = (EFI_FILE_INFO *)buf;
		if (!(fi->Attribute & EFI_FILE_DIRECTORY) || fi->FileName[0] == '.') continue;
		if (wieq(fi->FileName, u"BOOT") || wieq(fi->FileName, u"Microsoft") || wieq(fi->FileName, u"98REBORN")) continue;
		static const CHAR16 *loaders[] = {u"shimx64.efi", u"grubx64.efi", u"systemd-bootx64.efi"};
		for (int i = 0; i < 3; i++) {
			CHAR16 p[128] = u"\\EFI\\";
			wcat(p, fi->FileName, 128); wcat(p, u"\\", 128); wcat(p, loaders[i], 128);
			if (file_exists(root, p)) {
				CHAR16 t[64] = u"Linux (";
				CHAR16 name[32]; wcopy(name, fi->FileName, 32);
				if (name[0] >= 'a' && name[0] <= 'z') name[0] -= 32;
				wcat(t, name, 64); wcat(t, u")", 64);
				struct entry *e = add(K_EFI, t, u"Starts its own boot loader");
				if (e) { e->dev = dev; wcopy(e->path, p, 128); }
				break;
			}
		}
	}
	dir->Close(dir);
}

static void find_entries(void) {
	add(K_WIN98, u"Windows 98", u"Second Edition, with 98Reborn");
	UINTN n = 0; EFI_HANDLE *hs = 0;
	if (!EFI_ERROR(BS->LocateHandleBuffer(ByProtocol, &SimpleFsGuid, 0, &n, &hs))) {
		for (UINTN i = 0; i < n; i++) {
			EFI_FILE *root = open_root(hs[i]);
			if (!root) continue;
			if (file_exists(root, u"\\EFI\\Microsoft\\Boot\\bootmgfw.efi")) {
				struct entry *e = add(K_EFI, u"Windows", u"Windows Boot Manager");
				if (e) { e->dev = hs[i]; wcopy(e->path, u"\\EFI\\Microsoft\\Boot\\bootmgfw.efi", 128); }
			}
			scan_linux(root, hs[i]);
			root->Close(root);
		}
		BS->FreePool(hs);
	}
	add(K_FIRMWARE, u"Firmware settings", u"BIOS / UEFI setup");
	add(K_POWEROFF, u"Turn off", u"");
}

/* ---------- starting things ---------- */

static EFI_DEVICE_PATH *file_path(EFI_HANDLE dev, const CHAR16 *path) {
	EFI_DEVICE_PATH *base;
	if (EFI_ERROR(BS->HandleProtocol(dev, &DevicePathGuid, (void **)&base))) return 0;
	UINTN blen = 0;
	for (EFI_DEVICE_PATH *p = base; !(p->Type == 0x7f && p->SubType == 0xff);) {
		UINTN l = p->Length[0] | p->Length[1] << 8;
		blen += l;
		p = (EFI_DEVICE_PATH *)((u8 *)p + l);
	}
	UINTN plen = (wlen(path) + 1) * 2, flen = 4 + plen;
	u8 *dp = alloc(blen + flen + 4);
	if (!dp) return 0;
	memcpy(dp, base, blen);
	u8 *f = dp + blen;
	f[0] = 4; f[1] = 4; f[2] = (u8)flen; f[3] = (u8)(flen >> 8); /* media / file path */
	memcpy(f + 4, path, plen);
	u8 *end = f + flen;
	end[0] = 0x7f; end[1] = 0xff; end[2] = 4; end[3] = 0;
	return (EFI_DEVICE_PATH *)dp;
}

static EFI_STATUS start_efi(EFI_HANDLE dev, const CHAR16 *path) {
	EFI_DEVICE_PATH *dp = file_path(dev, path);
	if (!dp) return 0xF1; /* no device path for the volume */
	EFI_HANDLE img;
	EFI_STATUS s = BS->LoadImage(0, Self, dp, 0, 0, &img);
	if (EFI_ERROR(s)) return s;
	BS->SetWatchdogTimer(0, 0, 0, 0);
	return BS->StartImage(img, 0, 0);
}

/* ---------- drawing ---------- */

static EFI_GOP *gop;
static EFI_PIXEL *fb;
static u32 W, H;
static const struct font *ft_title, *ft_body, *ft_small;

static EFI_PIXEL rgb(u32 c) { EFI_PIXEL p = {(u8)c, (u8)(c >> 8), (u8)(c >> 16), 0}; return p; }

/* 98Reborn colors */
#define C_DESK 0x0b3b38
#define C_DESK2 0x0f4f4a
#define C_PANEL 0xf7f7f4
#define C_TITLE 0x0f766e
#define C_TEXT 0x16181d
#define C_MUTED 0x555b66
#define C_SEL 0xccfbf1
#define C_SELBAR 0x0f766e
#define C_WHITE 0xffffff
#define C_BEVEL_LO 0xa8a8a0
#define C_SCREEN 0x042f2e
#define C_GLOW 0x5eead4

static void rect(int x, int y, int w, int h, u32 c) {
	EFI_PIXEL p = rgb(c);
	for (int j = y < 0 ? 0 : y; j < y + h && j < (int)H; j++)
		for (int i = x < 0 ? 0 : x; i < x + w && i < (int)W; i++) fb[(UINTN)j * W + i] = p;
}

static int text(const struct font *f, int x, int y, const CHAR16 *s, u32 c) {
	EFI_PIXEL p = rgb(c);
	for (; *s; s++, x += f->w) {
		u32 ch = *s < 256 ? *s : '?';
		const u8 *g = f->bits + ch * f->stride * f->h;
		for (int r = 0; r < f->h; r++)
			for (int col = 0; col < f->w; col++)
				if (g[r * f->stride + col / 8] & (0x80 >> (col % 8))) {
					int px = x + col, py = y + r;
					if (px >= 0 && py >= 0 && px < (int)W && py < (int)H) fb[(UINTN)py * W + px] = p;
				}
	}
	return x;
}

static int text_w(const struct font *f, const CHAR16 *s) { return (int)wlen(s) * f->w; }

/* The 98Reborn mark: a CRT with a rebirth arc (same geometry as the site's
 * favicon, on a 64-unit grid). */
static void logo(int x, int y, int size) {
	int u = size;
#define S(v) ((v) * u / 64)
	/* Light frame: the mark sits on the teal title bar. */
	rect(x + S(6), y + S(8), S(52), S(38), C_SEL);
	rect(x + S(11), y + S(13), S(42), S(28), C_SCREEN);
	rect(x + S(24), y + S(46), S(16), S(5), C_SEL);
	rect(x + S(16), y + S(51), S(32), S(5), C_SEL);
	int cx = x + S(32), cy = y + S(33), ro = S(13), ri = S(9);
	for (int j = cy - ro; j <= cy; j++)
		for (int i = cx - ro; i <= cx + ro; i++) {
			int dx = i - cx, dy = j - cy, d = dx * dx + dy * dy;
			if (d <= ro * ro && d >= ri * ri) rect(i, j, 1, 1, C_GLOW);
		}
	for (int k = 0; k < S(6); k++) rect(x + S(38) + k / 2, y + S(27) + k, S(10) - k, 1, C_GLOW);
#undef S
}

static void present(void) {
	gop->Blt(gop, fb, EfiBltBufferToVideo, 0, 0, 0, 0, W, H, 0);
}

static void draw(int sel, int countdown, const CHAR16 *note) {
	/* Desktop: a soft vertical gradient. */
	for (u32 j = 0; j < H; j++) {
		u32 t = j * 255 / H;
		u32 r = (0x0b * (255 - t) + 0x06 * t) / 255, g = (0x3b * (255 - t) + 0x25 * t) / 255, b = (0x38 * (255 - t) + 0x23 * t) / 255;
		rect(0, j, W, 1, r << 16 | g << 8 | b);
	}
	int rowh = ft_body->h * 2 + 18;
	int pw = W > 900 ? 760 : W - 40, ph = 76 + nentries * rowh + 70;
	int px = ((int)W - pw) / 2, py = ((int)H - ph) / 2;
	/* Window with a 98-style bevel. */
	rect(px - 2, py - 2, pw + 4, ph + 4, C_BEVEL_LO);
	rect(px - 2, py - 2, pw + 2, 2, C_WHITE);
	rect(px - 2, py - 2, 2, ph + 2, C_WHITE);
	rect(px, py, pw, ph, C_PANEL);
	rect(px, py, pw, 56, C_TITLE);
	logo(px + 12, py + 6, 44);
	text(ft_title, px + 66, py + (56 - ft_title->h) / 2, u"98Reborn Boot", C_WHITE);
	int y = py + 76;
	for (int i = 0; i < nentries; i++) {
		struct entry *e = &entries[i];
		if (i == sel) {
			rect(px + 12, y - 6, pw - 24, rowh - 6, C_SEL);
			rect(px + 12, y - 6, 5, rowh - 6, C_SELBAR);
		}
		CHAR16 num[4] = {(CHAR16)('1' + i), '.', ' ', 0};
		text(ft_body, px + 30, y, num, C_MUTED);
		text(ft_body, px + 30 + 3 * ft_body->w, y, e->title, C_TEXT);
		if (e->detail[0]) text(ft_small, px + 30 + 3 * ft_body->w, y + ft_body->h + 2, e->detail, C_MUTED);
		y += rowh;
	}
	CHAR16 foot[128] = u"\x18\x19 choose   Enter start   F2 firmware setup";
	if (countdown >= 0) {
		CHAR16 n[12]; utoa(n, (u32)countdown);
		wcopy(foot, u"Starting ", 128); wcat(foot, entries[sel].title, 128); wcat(foot, u" in ", 128);
		wcat(foot, n, 128); wcat(foot, u"s.  Press any key to choose.", 128);
	}
	text(ft_small, px + 30, py + ph - 52, note && note[0] ? note : foot, note && note[0] ? 0x9a3412 : C_MUTED);
	text(ft_small, px + pw - 30 - text_w(ft_small, u"98reborn"), py + ph - 26, u"98reborn", C_BEVEL_LO);
	present();
}

static int setup_graphics(void) {
	if (EFI_ERROR(BS->LocateProtocol(&GopGuid, 0, (void **)&gop)) || !gop->Mode || !gop->Mode->Info) return 0;
	W = gop->Mode->Info->HorizontalResolution;
	H = gop->Mode->Info->VerticalResolution;
	fb = alloc((UINTN)W * H * sizeof(EFI_PIXEL));
	if (!fb) return 0;
	if (W >= 1600) { ft_title = &ter_132b; ft_body = &ter_124b; ft_small = &ter_120n; }
	else if (W >= 1000) { ft_title = &ter_124b; ft_body = &ter_120b; ft_small = &ter_116n; }
	else { ft_title = &ter_120b; ft_body = &ter_116n; ft_small = &ter_116n; }
	return 1;
}

/* ---------- text fallback ---------- */

static void tprint(const CHAR16 *s) { ST->ConOut->OutputString(ST->ConOut, s); }

static void draw_text(int sel, int countdown, const CHAR16 *note) {
	ST->ConOut->ClearScreen(ST->ConOut);
	tprint(u"98Reborn Boot\r\n\r\n");
	for (int i = 0; i < nentries; i++) {
		tprint(i == sel ? u" > " : u"   ");
		tprint(entries[i].title);
		tprint(u"\r\n");
	}
	if (note && note[0]) { tprint(u"\r\n"); tprint(note); }
	else if (countdown >= 0) { CHAR16 n[12]; utoa(n, (u32)countdown); tprint(u"\r\nStarting in "); tprint(n); tprint(u"s"); }
	tprint(u"\r\n");
}

/* ---------- actions ---------- */

static CHAR16 note[160];

static void say(const CHAR16 *s) { wcopy(note, s, 160); }

/* Appends " (code 0x...)" so failures can be reported precisely. */
static void say_status(const CHAR16 *s, EFI_STATUS st) {
	say(s);
	CHAR16 h[24] = u" (code 0x";
	int n = wlen(h);
	int started = 0;
	for (int i = 60; i >= 0; i -= 4) {
		int d = (st >> i) & 15;
		if (d || started || i == 0) { h[n++] = d < 10 ? '0' + d : 'a' + d - 10; started = 1; }
	}
	h[n++] = ')'; h[n] = 0;
	wcat(note, h, 160);
}

static void boot_win98(EFI_HANDLE selfdev) {
	EFI_FILE *root = open_root(selfdev);
	int has_rebios = root && file_exists(root, u"\\EFI\\98REBORN\\REBIOS.EFI");
	int has_csmwrap = root && file_exists(root, u"\\EFI\\98REBORN\\CSMWRAP.EFI");
	if (root) root->Close(root);
	int want_csm = cfg_engine[0] == 'c';
	if (has_rebios && !want_csm) {
		start_efi(selfdev, u"\\EFI\\98REBORN\\REBIOS.EFI");
		/* ReBIOS only returns when it can't run on this PC. */
		if (cfg_engine[0] == 'r') { say(u"ReBIOS couldn't start: turn on VT-x/AMD-V and the IOMMU (VT-d/AMD-Vi) in the firmware."); return; }
	}
	if (has_csmwrap) {
		EFI_STATUS st = start_efi(selfdev, u"\\EFI\\98REBORN\\CSMWRAP.EFI");
		say_status(u"Compatibility mode (CSMWrap) couldn't start Windows 98 on this PC.", st);
		return;
	}
	say(u"Windows 98 can't start: \\EFI\\98REBORN is missing its BIOS layer. Run the installer again.");
}

static void firmware_setup(void) {
	u64 ind = EFI_OS_INDICATIONS_BOOT_TO_FW_UI;
	u64 sup = 0; UINTN n = sizeof sup;
	if (EFI_ERROR(ST->RuntimeServices->GetVariable(u"OsIndicationsSupported", &GlobalVarGuid, 0, &n, &sup)) || !(sup & ind)) {
		say(u"This PC doesn't support opening firmware setup from here. Use its setup key (often F2 or Del) at power-on.");
		return;
	}
	ST->RuntimeServices->SetVariable(u"OsIndications", &GlobalVarGuid,
		EFI_VARIABLE_NON_VOLATILE | EFI_VARIABLE_BOOTSERVICE_ACCESS | EFI_VARIABLE_RUNTIME_ACCESS, sizeof ind, &ind);
	ST->RuntimeServices->ResetSystem(EfiResetCold, 0, 0, 0);
}

static void run(int i, EFI_HANDLE selfdev) {
	struct entry *e = &entries[i];
	note[0] = 0;
	switch (e->kind) {
	case K_WIN98: boot_win98(selfdev); break;
	case K_EFI:
		if (EFI_ERROR(start_efi(e->dev, e->path))) say(u"That system's boot loader couldn't be started.");
		break;
	case K_FIRMWARE: firmware_setup(); break;
	case K_POWEROFF: ST->RuntimeServices->ResetSystem(EfiResetShutdown, 0, 0, 0); break;
	}
}

EFI_STATUS efi_main(EFI_HANDLE image, EFI_SYSTEM_TABLE *st) {
	ST = st; BS = st->BootServices; Self = image;
	BS->SetWatchdogTimer(0, 0, 0, 0);
	EFI_LOADED_IMAGE *li;
	if (EFI_ERROR(BS->HandleProtocol(image, &LoadedImageGuid, (void **)&li))) return 1;
	EFI_FILE *root = open_root(li->DeviceHandle);
	load_config(root);
	if (root) root->Close(root);
	find_entries();

	int sel = 0;
	for (int i = 0; i < nentries; i++)
		if (wieq(entries[i].title, cfg_default) || (i == 0 && wieq(cfg_default, u"win98"))) { sel = i; break; }

	int gfx = setup_graphics();
	ST->ConOut->EnableCursor(ST->ConOut, 0);
	int countdown = (int)cfg_timeout;
	if (countdown == 0) { run(sel, li->DeviceHandle); countdown = -1; }
	for (;;) {
		if (gfx) draw(sel, countdown, note); else draw_text(sel, countdown, note);
		EFI_INPUT_KEY k;
		/* Wait up to a second for a key (the countdown's tick). */
		int got = 0;
		for (int t = 0; t < 100; t++) {
			if (ST->ConIn->ReadKeyStroke(ST->ConIn, &k) == EFI_SUCCESS) { got = 1; break; }
			BS->Stall(10000);
		}
		if (!got) {
			if (countdown > 0 && --countdown == 0) { run(sel, li->DeviceHandle); countdown = -1; }
			continue;
		}
		countdown = -1;
		if (k.ScanCode == SCAN_UP) sel = (sel + nentries - 1) % nentries;
		else if (k.ScanCode == SCAN_DOWN) sel = (sel + 1) % nentries;
		else if (k.ScanCode == SCAN_F2) firmware_setup();
		else if (k.UnicodeChar >= '1' && k.UnicodeChar < '1' + nentries) { sel = k.UnicodeChar - '1'; run(sel, li->DeviceHandle); }
		else if (k.UnicodeChar == '\r' || k.UnicodeChar == ' ') run(sel, li->DeviceHandle);
	}
}
