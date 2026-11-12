package backend

import (
	"os"
	"testing"
)

type fsInfo struct{ bytesPerSector, sectorsPerUnit, total, avail uint64 }

func (f fsInfo) BlockSize() uint64           { return f.bytesPerSector }
func (f fsInfo) FragmentSize() uint64        { return f.sectorsPerUnit }
func (f fsInfo) TotalBlockCount() uint64     { return f.total }
func (f fsInfo) FreeBlockCount() uint64      { return f.avail }
func (f fsInfo) AvailableBlockCount() uint64 { return f.avail }

// TestSMBSpace: a 1 TB share with 2 sectors of 512 bytes per allocation
// unit (as a Hetzner Storage Box reports it).
func TestSMBSpace(t *testing.T) {
	sp := smbSpace(fsInfo{bytesPerSector: 512, sectorsPerUnit: 2, total: 1 << 30, avail: 600 << 20})
	if sp.Total != 1<<40 || sp.Free != 600<<30 {
		t.Fatalf("%+v", sp)
	}
	if sp := smbSpace(fsInfo{bytesPerSector: 4096, sectorsPerUnit: 0, total: 10}); sp.Total != 40960 {
		t.Fatalf("no sectors per unit: %+v", sp)
	}
}

func TestLocalSpace(t *testing.T) {
	dir := t.TempDir()
	sp, err := NewLocal(dir + "/not/yet/created").Space(nil)
	if err != nil || sp.Total == 0 || sp.Free > sp.Total {
		t.Fatalf("%+v %v", sp, err)
	}
	os.Remove(dir)
}
