//go:build darwin

package daemonprocess

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"unsafe"

	"github.com/ebitengine/purego"
)

const (
	darwinProcPIDRegionPathInfo = 8
	darwinVMProtectionExecute   = 0x04
	darwinMaxExecutableRegions  = 16384
)

// These structures mirror proc_regionwithpathinfo and its nested libproc ABI
// structures. Keeping the vnode identity from the process mapping lets us
// reject an executable pathname that was swapped after the process started.
type darwinProcRegionInfo struct {
	Protection         uint32
	MaxProtection      uint32
	Inheritance        uint32
	Flags              uint32
	Offset             uint64
	Behavior           uint32
	UserWiredCount     uint32
	UserTag            uint32
	PagesResident      uint32
	PagesSharedPrivate uint32
	PagesSwappedOut    uint32
	PagesDirtied       uint32
	RefCount           uint32
	ShadowDepth        uint32
	ShareMode          uint32
	PrivatePages       uint32
	SharedPages        uint32
	ObjectID           uint32
	Depth              uint32
	Address            uint64
	Size               uint64
}

type darwinVinfoStat struct {
	Dev         uint32
	Mode        uint16
	Nlink       uint16
	Ino         uint64
	UID         uint32
	GID         uint32
	ATime       int64
	ATimeNsec   int64
	MTime       int64
	MTimeNsec   int64
	CTime       int64
	CTimeNsec   int64
	BirthTime   int64
	BirthTimeNS int64
	Size        int64
	Blocks      int64
	BlockSize   int32
	Flags       uint32
	Generation  uint32
	Rdev        uint32
	QuadSpare   [2]int64
}

type darwinVnodeInfo struct {
	Stat darwinVinfoStat
	Type int32
	Pad  int32
	FSID [2]int32
}

type darwinVnodeInfoPath struct {
	Info darwinVnodeInfo
	Path [1024]byte
}

type darwinProcRegionWithPathInfo struct {
	Region darwinProcRegionInfo
	Vnode  darwinVnodeInfoPath
}

type darwinProcessExecutableAPI struct {
	procPIDInfo      func(int32, int32, uint64, unsafe.Pointer, int32) int32
	procPIDPathToken func(*darwinAuditToken, unsafe.Pointer, uint32) int32
}

var (
	darwinExecutableAPIOnce sync.Once
	darwinExecutableAPI     darwinProcessExecutableAPI
	darwinExecutableAPIErr  error
)

func loadDarwinProcessExecutableAPI() (darwinProcessExecutableAPI, error) {
	darwinExecutableAPIOnce.Do(func() {
		handle, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_LAZY|purego.RTLD_LOCAL)
		if err != nil {
			darwinExecutableAPIErr = err
			return
		}
		for _, symbol := range []struct {
			name   string
			target any
		}{
			{name: "proc_pidinfo", target: &darwinExecutableAPI.procPIDInfo},
			{name: "proc_pidpath_audittoken", target: &darwinExecutableAPI.procPIDPathToken},
		} {
			address, lookupErr := purego.Dlsym(handle, symbol.name)
			if lookupErr != nil {
				darwinExecutableAPIErr = fmt.Errorf("load %s for process-bound executable capture: %w", symbol.name, lookupErr)
				return
			}
			purego.RegisterFunc(symbol.target, address)
		}
	})
	return darwinExecutableAPI, darwinExecutableAPIErr
}

func openPlatformProcessExecutable(owner processIdentity) (*os.File, error) {
	api, err := loadDarwinProcessExecutableAPI()
	if err != nil {
		return nil, err
	}
	token, err := captureDarwinAuditToken(owner.pid)
	if err != nil {
		return nil, fmt.Errorf("capture process audit token: %w", err)
	}

	var pathBuffer [4096]byte
	if count := api.procPIDPathToken(&token, unsafe.Pointer(&pathBuffer[0]), uint32(len(pathBuffer))); count <= 0 {
		return nil, fmt.Errorf("read audit-token-bound executable path for pid %d", owner.pid)
	}
	boundPath := nulTerminatedString(pathBuffer[:])
	if boundPath == "" {
		return nil, fmt.Errorf("audit-token-bound executable path for pid %d is empty", owner.pid)
	}
	file, err := os.Open(boundPath)
	if err != nil {
		return nil, fmt.Errorf("open audit-token-bound executable path %q: %w", boundPath, err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("inspect audit-token-bound executable path %q: %w", boundPath, err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		_ = file.Close()
		return nil, fmt.Errorf("inspect vnode identity for executable path %q", boundPath)
	}

	matched, err := darwinExecutableMappingMatches(api, owner.pid, boundPath, uint32(stat.Dev), stat.Ino)
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if !matched {
		_ = file.Close()
		return nil, fmt.Errorf("opened executable path %q does not match the vnode mapped by pid %d", boundPath, owner.pid)
	}
	return file, nil
}

func darwinExecutableMappingMatches(api darwinProcessExecutableAPI, pid int, executablePath string, device uint32, inode uint64) (bool, error) {
	address := uint64(0)
	wantedSize := int32(unsafe.Sizeof(darwinProcRegionWithPathInfo{}))
	for range darwinMaxExecutableRegions {
		var region darwinProcRegionWithPathInfo
		got := api.procPIDInfo(int32(pid), darwinProcPIDRegionPathInfo, address, unsafe.Pointer(&region), wantedSize)
		if got <= 0 {
			return false, nil
		}
		if got != wantedSize {
			return false, fmt.Errorf("read executable mappings for pid %d: got %d bytes, want %d", pid, got, wantedSize)
		}
		if region.Region.Protection&darwinVMProtectionExecute != 0 &&
			filepath.Clean(nulTerminatedString(region.Vnode.Path[:])) == filepath.Clean(executablePath) &&
			region.Vnode.Info.Stat.Dev == device && region.Vnode.Info.Stat.Ino == inode {
			return true, nil
		}
		next := region.Region.Address + region.Region.Size
		if next <= address {
			return false, fmt.Errorf("read executable mappings for pid %d: region enumeration did not advance", pid)
		}
		address = next
	}
	return false, fmt.Errorf("read executable mappings for pid %d: exceeded region limit", pid)
}

func nulTerminatedString(buffer []byte) string {
	if index := bytes.IndexByte(buffer, 0); index >= 0 {
		buffer = buffer[:index]
	}
	return string(buffer)
}
