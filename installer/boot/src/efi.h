/* Minimal UEFI definitions for 98Reborn Boot, written from the UEFI 2.x
 * specification. Only what the boot menu uses is typed; other table slots
 * are placeholders so the layouts match. x86-64, Microsoft calling
 * convention (the default for the x86_64-unknown-windows target). */
#pragma once

typedef unsigned char u8;
typedef unsigned short u16;
typedef unsigned int u32;
typedef unsigned long long u64;
typedef long long i64;
typedef u64 UINTN;
typedef u64 EFI_STATUS;
typedef void *EFI_HANDLE;
typedef void *EFI_EVENT;
typedef u16 CHAR16;

#define EFI_SUCCESS 0
#define EFI_ERROR(s) ((i64)(s) < 0)
#define EFI_BUFFER_TOO_SMALL 0x8000000000000005ULL
#define EFI_NOT_READY 0x8000000000000006ULL

typedef struct { u32 a; u16 b, c; u8 d[8]; } EFI_GUID;

typedef struct {
	u64 Signature;
	u32 Revision, HeaderSize, CRC32, Reserved;
} EFI_TABLE_HEADER;

/* Console */
typedef struct { u16 ScanCode; CHAR16 UnicodeChar; } EFI_INPUT_KEY;
#define SCAN_UP 0x01
#define SCAN_DOWN 0x02
#define SCAN_F2 0x0C
#define SCAN_ESC 0x17

typedef struct EFI_SIMPLE_TEXT_INPUT {
	EFI_STATUS (*Reset)(struct EFI_SIMPLE_TEXT_INPUT *, u8);
	EFI_STATUS (*ReadKeyStroke)(struct EFI_SIMPLE_TEXT_INPUT *, EFI_INPUT_KEY *);
	EFI_EVENT WaitForKey;
} EFI_SIMPLE_TEXT_INPUT;

typedef struct EFI_SIMPLE_TEXT_OUTPUT {
	EFI_STATUS (*Reset)(struct EFI_SIMPLE_TEXT_OUTPUT *, u8);
	EFI_STATUS (*OutputString)(struct EFI_SIMPLE_TEXT_OUTPUT *, const CHAR16 *);
	void *TestString, *QueryMode, *SetMode;
	EFI_STATUS (*SetAttribute)(struct EFI_SIMPLE_TEXT_OUTPUT *, UINTN);
	EFI_STATUS (*ClearScreen)(struct EFI_SIMPLE_TEXT_OUTPUT *);
	void *SetCursorPosition;
	EFI_STATUS (*EnableCursor)(struct EFI_SIMPLE_TEXT_OUTPUT *, u8);
	void *Mode;
} EFI_SIMPLE_TEXT_OUTPUT;

/* Device paths */
typedef struct { u8 Type, SubType, Length[2]; } EFI_DEVICE_PATH;

/* Files */
#define EFI_FILE_MODE_READ 1ULL
#define EFI_FILE_DIRECTORY 0x10ULL

typedef struct EFI_FILE {
	u64 Revision;
	EFI_STATUS (*Open)(struct EFI_FILE *, struct EFI_FILE **, const CHAR16 *, u64, u64);
	EFI_STATUS (*Close)(struct EFI_FILE *);
	void *Delete;
	EFI_STATUS (*Read)(struct EFI_FILE *, UINTN *, void *);
	void *Write, *GetPosition, *SetPosition;
	EFI_STATUS (*GetInfo)(struct EFI_FILE *, const EFI_GUID *, UINTN *, void *);
	void *SetInfo, *Flush;
} EFI_FILE;

typedef struct {
	u64 Size, FileSize, PhysicalSize;
	u8 CreateTime[16], LastAccessTime[16], ModificationTime[16];
	u64 Attribute;
	CHAR16 FileName[1];
} EFI_FILE_INFO;

typedef struct EFI_SIMPLE_FILE_SYSTEM {
	u64 Revision;
	EFI_STATUS (*OpenVolume)(struct EFI_SIMPLE_FILE_SYSTEM *, EFI_FILE **);
} EFI_SIMPLE_FILE_SYSTEM;

/* Graphics */
typedef struct { u8 Blue, Green, Red, Reserved; } EFI_PIXEL;

typedef struct {
	u32 Version, HorizontalResolution, VerticalResolution, PixelFormat;
	u32 RedMask, GreenMask, BlueMask, ReservedMask;
	u32 PixelsPerScanLine;
} EFI_GOP_MODE_INFO;

typedef struct {
	u32 MaxMode, Mode;
	EFI_GOP_MODE_INFO *Info;
	UINTN SizeOfInfo;
	u64 FrameBufferBase;
	UINTN FrameBufferSize;
} EFI_GOP_MODE;

#define EfiBltBufferToVideo 2

typedef struct EFI_GOP {
	EFI_STATUS (*QueryMode)(struct EFI_GOP *, u32, UINTN *, EFI_GOP_MODE_INFO **);
	EFI_STATUS (*SetMode)(struct EFI_GOP *, u32);
	EFI_STATUS (*Blt)(struct EFI_GOP *, EFI_PIXEL *, u32, UINTN, UINTN, UINTN, UINTN, UINTN, UINTN, UINTN);
	EFI_GOP_MODE *Mode;
} EFI_GOP;

/* Loaded image */
typedef struct {
	u32 Revision;
	EFI_HANDLE ParentHandle;
	void *SystemTable;
	EFI_HANDLE DeviceHandle;
	EFI_DEVICE_PATH *FilePath;
	void *Reserved;
	u32 LoadOptionsSize;
	void *LoadOptions;
	void *ImageBase;
	u64 ImageSize;
	u32 ImageCodeType, ImageDataType;
	void *Unload;
} EFI_LOADED_IMAGE;

/* Services */
#define ByProtocol 2
#define EfiLoaderData 2
#define EfiResetCold 0
#define EfiResetShutdown 2

typedef struct {
	EFI_TABLE_HEADER Hdr;
	void *RaiseTPL, *RestoreTPL;
	void *AllocatePages, *FreePages, *GetMemoryMap;
	EFI_STATUS (*AllocatePool)(u32, UINTN, void **);
	EFI_STATUS (*FreePool)(void *);
	void *CreateEvent, *SetTimer, *WaitForEvent, *SignalEvent, *CloseEvent, *CheckEvent;
	void *InstallProtocolInterface, *ReinstallProtocolInterface, *UninstallProtocolInterface;
	EFI_STATUS (*HandleProtocol)(EFI_HANDLE, const EFI_GUID *, void **);
	void *Reserved, *RegisterProtocolNotify, *LocateHandle, *LocateDevicePath, *InstallConfigurationTable;
	EFI_STATUS (*LoadImage)(u8, EFI_HANDLE, EFI_DEVICE_PATH *, void *, UINTN, EFI_HANDLE *);
	EFI_STATUS (*StartImage)(EFI_HANDLE, UINTN *, CHAR16 **);
	void *Exit;
	EFI_STATUS (*UnloadImage)(EFI_HANDLE);
	void *ExitBootServices, *GetNextMonotonicCount;
	EFI_STATUS (*Stall)(UINTN);
	EFI_STATUS (*SetWatchdogTimer)(UINTN, u64, UINTN, CHAR16 *);
	void *ConnectController, *DisconnectController;
	void *OpenProtocol, *CloseProtocol, *OpenProtocolInformation;
	void *ProtocolsPerHandle;
	EFI_STATUS (*LocateHandleBuffer)(u32, const EFI_GUID *, void *, UINTN *, EFI_HANDLE **);
	EFI_STATUS (*LocateProtocol)(const EFI_GUID *, void *, void **);
	void *InstallMultipleProtocolInterfaces, *UninstallMultipleProtocolInterfaces;
	void *CalculateCrc32, *CopyMem, *SetMem, *CreateEventEx;
} EFI_BOOT_SERVICES;

typedef struct {
	EFI_TABLE_HEADER Hdr;
	void *GetTime, *SetTime, *GetWakeupTime, *SetWakeupTime, *SetVirtualAddressMap, *ConvertPointer;
	EFI_STATUS (*GetVariable)(const CHAR16 *, const EFI_GUID *, u32 *, UINTN *, void *);
	void *GetNextVariableName;
	EFI_STATUS (*SetVariable)(const CHAR16 *, const EFI_GUID *, u32, UINTN, void *);
	void *GetNextHighMonotonicCount;
	void (*ResetSystem)(u32, EFI_STATUS, UINTN, void *);
} EFI_RUNTIME_SERVICES;

typedef struct {
	EFI_TABLE_HEADER Hdr;
	CHAR16 *FirmwareVendor;
	u32 FirmwareRevision;
	EFI_HANDLE ConsoleInHandle;
	EFI_SIMPLE_TEXT_INPUT *ConIn;
	EFI_HANDLE ConsoleOutHandle;
	EFI_SIMPLE_TEXT_OUTPUT *ConOut;
	EFI_HANDLE StandardErrorHandle;
	EFI_SIMPLE_TEXT_OUTPUT *StdErr;
	EFI_RUNTIME_SERVICES *RuntimeServices;
	EFI_BOOT_SERVICES *BootServices;
	UINTN NumberOfTableEntries;
	void *ConfigurationTable;
} EFI_SYSTEM_TABLE;

#define EFI_VARIABLE_NON_VOLATILE 1
#define EFI_VARIABLE_BOOTSERVICE_ACCESS 2
#define EFI_VARIABLE_RUNTIME_ACCESS 4
#define EFI_OS_INDICATIONS_BOOT_TO_FW_UI 1ULL
