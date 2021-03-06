package runtime

import (
	"runtime/internal/sys"
	"unsafe"
)

const (
	_PAGESIZE = 0x1000
)

var (
	_atman_hypercall_page   [2 * _PAGESIZE]byte
	_atman_shared_info_page [2 * _PAGESIZE]byte

	_atman_start_info  = &xenStartInfo{}
	_atman_shared_info *xenSharedInfo

	_atman_page_frame_list pageFrameList
)

//go:nosplit
func getRandomData(r []byte) {
	extendRandom(r, 0)
}

// env

func gogetenv(key string) string { return "" }

var _cgo_setenv unsafe.Pointer   // pointer to C function
var _cgo_unsetenv unsafe.Pointer // pointer to C function

// signals

const _NSIG = 0

func initsig(bool)             {}
func sigdisable(uint32)        {}
func sigenable(uint32)         {}
func sigignore(uint32)         {}
func raisebadsignal(sig int32) {}

// net

func netpoll(block bool) *g { return nil }
func netpollinited() bool   { return false }

type xenStartInfo struct {
	Magic          [32]byte
	NrPages        uint64
	SharedInfoAddr uintptr // machine address of share info struct
	SIFFlags       uint32
	_              [4]byte

	Store struct {
		Mfn      mfn // machine page number of shared page
		Eventchn uint32
		_        [4]byte
	}

	Console struct {
		Mfn      mfn    // machine page number of console page
		Eventchn uint32 // event channel
		_        [4]byte
	}

	PageTableBase     vaddr // virtual address of page directory
	NrPageTableFrames uint64
	PageFrameList     uintptr // virtual address of page-frame list
	ModStart          uintptr // virtual address of pre-loaded module
	ModLen            uint64  // size (bytes) of pre-loaded module
	CmdLine           [1024]byte

	// The pfn range here covers both page table and p->m table frames
	FirstP2mPfn uint64 // 1st pfn forming initial P->M table
	NrP2mFrames uint64 // # of pgns forming initial P->M table
}

type xenSharedInfo struct {
	VCPUInfo      [32]vcpuInfo
	EvtchnPending [64]uint64
	EvtchnMask    [64]uint64
	WcVersion     uint32
	WcSec         uint32
	WcNsec        uint32
	_             [4]byte
	Arch          archSharedInfo
}

type archSharedInfo struct {
	MaxPfn                uint64
	PfnToMfnFrameListList uint64
	NmiReason             uint64
	_                     [32]uint64
}

type archVCPUInfo struct {
	CR2 uint64
	_   uint64
}

type vcpuTimeInfo struct {
	Version    uint32
	_          uint32
	TSC        uint64
	SystemNsec uint64
	TSCMul     uint32
	TSCShift   int8
	_          [3]int8
}

type vcpuInfo struct {
	UpcallPending uint8
	UpcallMask    uint8
	_             [6]byte
	PendingSel    uint64
	Arch          archVCPUInfo
	Time          vcpuTimeInfo
}

func atmaninit() {
	_atman_console.init()

	println("Atman OS")
	println("     ptr_size: ", sys.PtrSize)
	println("   start_info: ", _atman_start_info)
	println("        magic: ", string(_atman_start_info.Magic[:]))
	println("     nr_pages: ", _atman_start_info.NrPages)
	println("  shared_info: ", _atman_start_info.SharedInfoAddr)
	println("   siff_flags: ", _atman_start_info.SIFFlags)
	println("    store_mfn: ", _atman_start_info.Store.Mfn)
	println("    store_evc: ", _atman_start_info.Store.Eventchn)
	println("  console_mfn: ", _atman_start_info.Console.Mfn)
	println("  console_evc: ", _atman_start_info.Console.Eventchn)
	println("      pt_base: ", _atman_start_info.PageTableBase)
	println(" nr_pt_frames: ", _atman_start_info.NrPageTableFrames)
	println("     pfn_list: ", _atman_start_info.PageFrameList)
	println("    mod_start: ", _atman_start_info.ModStart)
	println("      mod_len: ", _atman_start_info.ModLen)
	println("     cmd_line: ", _atman_start_info.CmdLine[:])
	println("    first_pfn: ", _atman_start_info.FirstP2mPfn)
	println("nr_p2m_frames: ", _atman_start_info.NrP2mFrames)

	initSlice(
		unsafe.Pointer(&_atman_page_frame_list),
		unsafe.Pointer(_atman_start_info.PageFrameList),
		int(_atman_start_info.NrPages),
	)

	println("mapping _atman_start_info")
	mapSharedInfo(_atman_start_info.SharedInfoAddr)

	_atman_mm.init()

	initEvents()
}

//go:nosplit
func crash()

func mapSharedInfo(vaddr uintptr) {
	pageAddr := round(
		uintptr(unsafe.Pointer(&_atman_shared_info_page[0])),
		_PAGESIZE,
	)

	ret := HYPERVISOR_update_va_mapping(
		pageAddr,
		vaddr|7,
		2, // UVMF_INVLPG: flush only one entry
	)

	if ret != 0 {
		println("HYPERVISOR_update_va_mapping returned ", ret)
		panic("HYPERVISOR_update_va_mapping failed")
	}

	_atman_shared_info = (*xenSharedInfo)(unsafe.Pointer(pageAddr))
}

// initSlice makes the slice s point to array,
// with a length and capacity of len.
func initSlice(s, array unsafe.Pointer, len int) {
	sp := (*slice)(s)
	sp.array = array
	sp.len = len
	sp.cap = len
}

// memory management

// pageFrameList is an array of machine frame numbers
// indexed by page frame numbers.
type pageFrameList []mfn

func (l pageFrameList) Get(n pfn) mfn {
	return l[int(n)]
}

// Entry in level 3, 2, or 1 page table.
//
// - 63 if set means No execute (NX)
// - 51-13 the machine frame number
// - 12 available for guest
// - 11 available for guest
// - 10 available for guest
// - 9 available for guest
// - 8 global
// - 7 PAT (PSE is disabled, must use hypercall to make 4MB or 2MB pages)
// - 6 dirty
// - 5 accessed
// - 4 page cached disabled
// - 3 page write through
// - 2 userspace accessible
// - 1 writeable
// - 0 present
type pageTableEntry uintptr

const (
	xenPageTablePresent = 1 << iota
	xenPageTableWritable
	xenPageTableUserspaceAccessible
	xenPageTablePageWriteThrough
	xenPageTablePageCacheDisabled
	xenPageTableAccessed
	xenPageTableDirty
	xenPageTablePAT
	xenPageTableGlobal
	xenPageTableGuest1
	xenPageTableGuest2
	xenPageTableGuest3
	xenPageTableGuest4
	xenPageTableNoExecute = 1 << 63

	xenPageAddrMask  = 1<<52 - 1
	xenPageMask      = 1<<12 - 1
	xenPageFlagShift = 12

	PTE_PAGE_FLAGS       = xenPageTablePresent | xenPageTableWritable | xenPageTableUserspaceAccessible | xenPageTableAccessed
	PTE_PAGE_TABLE_FLAGS = xenPageTablePresent | xenPageTableUserspaceAccessible | xenPageTableAccessed | xenPageTableDirty
	PTE_TEMP             = xenPageTableGuest1
)

func (e pageTableEntry) debug() {
	println(
		"PTE<", unsafe.Pointer(e), ">:",
		" MFN=", e.mfn(),
		"  NX=", e.hasFlag(xenPageTableNoExecute),
		"   G=", e.hasFlag(xenPageTableGlobal),
		" PAT=", e.hasFlag(xenPageTablePAT),
		" DIR=", e.hasFlag(xenPageTableDirty),
		"   A=", e.hasFlag(xenPageTableAccessed),
		" PCD=", e.hasFlag(xenPageTablePageCacheDisabled),
		" PWT=", e.hasFlag(xenPageTablePageWriteThrough),
		"   U=", e.hasFlag(xenPageTableUserspaceAccessible),
		"   W=", e.hasFlag(xenPageTableWritable),
		"   P=", e.hasFlag(xenPageTablePresent),
	)
}

func (e *pageTableEntry) setFlag(f uintptr) {
	*e = pageTableEntry(uintptr(*e) | f)
}

func (e pageTableEntry) hasFlag(f uintptr) bool {
	return uintptr(e)&f == f
}

func (e pageTableEntry) mfn() mfn {
	return mfn((uintptr(e) & (xenPageAddrMask &^ xenPageMask)) >> xenPageFlagShift)
}

func (e pageTableEntry) vaddr() vaddr {
	return vaddr(e.pfn() << xenPageFlagShift)
}

func (e pageTableEntry) pfn() pfn {
	const (
		m2p xenMachineToPhysicalMap = 0xFFFF800000000000
	)

	return m2p.Get(e.mfn())
}

type xenPageTable uintptr

func (t xenPageTable) Get(i int) pageTableEntry {
	return *(*pageTableEntry)(add(unsafe.Pointer(t), uintptr(i)*sys.PtrSize))
}

func (t xenPageTable) vaddr() vaddr {
	return vaddr(t)
}

func newXenPageTable(vaddr vaddr) xenPageTable {
	return xenPageTable(vaddr)
}

type xenMachineToPhysicalMap uintptr

func (m2p xenMachineToPhysicalMap) Get(mfn mfn) pfn {
	offset := uintptr(mfn) * sys.PtrSize

	return pfn(*(*uintptr)(add(unsafe.Pointer(m2p), offset)))
}

type pageTableLevel int

func (l pageTableLevel) shift() uint64 {
	return 12 + uint64(l)*9
}

// mask returns a mask for the pageTableLevel l.
// It's undefined if l is pageTableLevel4.
func (l pageTableLevel) mask() uint64 {
	return (1 << (l + 1).shift()) - 1
}

const (
	pageTableLevel1 pageTableLevel = iota
	pageTableLevel2
	pageTableLevel3
	pageTableLevel4
)

func (a vaddr) pageTableOffset(level pageTableLevel) int {
	return int((a >> level.shift()) & (512 - 1))
}

type pfn uint64

func (n pfn) vaddr() vaddr {
	return vaddr(n << 12)
}

func (n pfn) add(v uint64) pfn {
	return n + pfn(v)
}

func (n pfn) mfn() mfn {
	return _atman_page_frame_list.Get(n)
}

type mfn uintptr

func (m mfn) pfn() pfn {
	const (
		m2p xenMachineToPhysicalMap = 0xFFFF800000000000
	)

	return m2p.Get(m)
}

type vaddr uintptr

func (a vaddr) pfn() pfn {
	return pfn((uint64(a) + _PAGESIZE - 1) >> 12)
}
