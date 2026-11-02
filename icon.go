package main

import (
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	// 单个图标数据上限，避免异常文件占用过多内存
	maxIconEntryBytes = 1 << 20 // 1 MiB
	// .ico 文件上限
	maxICOFileBytes = 4 << 20 // 4 MiB
)

// 支持读取内嵌图标的文件类型
var iconExts = map[string]bool{
	".ico": true,
	".exe": true,
	".dll": true,
	".scr": true,
	".cpl": true,
	".ocx": true,
}

func hasEmbeddedIcon(name string) bool {
	return iconExts[strings.ToLower(filepath.Ext(name))]
}

// 读取文件内嵌图标；.ico 直接返回，PE 文件（exe/dll 等）提取资源中的图标并封装成 ICO
func loadEmbeddedIcon(path string) ([]byte, bool) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".ico":
		return loadICO(path)
	case ".exe", ".dll", ".scr", ".cpl", ".ocx":
		return loadPEIcon(path)
	default:
		return nil, false
	}
}

func loadICO(path string) ([]byte, bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil || info.Size() < 6 || info.Size() > maxICOFileBytes {
		return nil, false
	}
	data := make([]byte, int(info.Size()))
	if _, err := io.ReadFull(f, data); err != nil {
		return nil, false
	}
	if data[0] != 0 || data[1] != 0 || data[2] != 1 || data[3] != 0 {
		return nil, false
	}
	if binary.LittleEndian.Uint16(data[4:6]) == 0 {
		return nil, false
	}
	return data, true
}

type peSection struct {
	va      uint32
	vsize   uint32
	rawPtr  uint32
	rawSize uint32
}

type resEntry struct {
	id     uint32
	off    uint32
	named  bool
	subdir bool
}

// 从 PE 文件（exe/dll 等）的 RT_GROUP_ICON 资源中提取一个图标
func loadPEIcon(path string) ([]byte, bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil || info.Size() < 64 {
		return nil, false
	}
	size := info.Size()

	dos := make([]byte, 64)
	if _, err := f.ReadAt(dos, 0); err != nil {
		return nil, false
	}
	if dos[0] != 'M' || dos[1] != 'Z' {
		return nil, false
	}
	peOff := int64(binary.LittleEndian.Uint32(dos[0x3c:]))
	if peOff > size-24 {
		return nil, false
	}

	coff := make([]byte, 24)
	if _, err := f.ReadAt(coff, peOff); err != nil {
		return nil, false
	}
	if string(coff[:4]) != "PE\x00\x00" {
		return nil, false
	}
	numSections := int(binary.LittleEndian.Uint16(coff[6:8]))
	optSize := int(binary.LittleEndian.Uint16(coff[20:22]))
	if numSections < 1 || numSections > 96 || optSize < 2 || optSize > 1<<15 {
		return nil, false
	}

	optOff := peOff + 24
	if optOff > size-int64(optSize) {
		return nil, false
	}
	opt := make([]byte, optSize)
	if _, err := f.ReadAt(opt, optOff); err != nil {
		return nil, false
	}

	var ddOff int
	switch binary.LittleEndian.Uint16(opt[:2]) {
	case 0x10b: // PE32
		ddOff = 96
	case 0x20b: // PE32+
		ddOff = 112
	default:
		return nil, false
	}
	if ddOff+24 > len(opt) {
		return nil, false
	}
	resRVA := binary.LittleEndian.Uint32(opt[ddOff+16:])
	resSize := binary.LittleEndian.Uint32(opt[ddOff+20:])
	if resRVA == 0 || resSize == 0 {
		return nil, false
	}

	sections := make([]peSection, 0, numSections)
	secOff := optOff + int64(optSize)
	secBuf := make([]byte, 40)
	for i := 0; i < numSections; i++ {
		off := secOff + int64(i)*40
		if off < 0 || off > size-40 {
			break
		}
		if _, err := f.ReadAt(secBuf, off); err != nil {
			break
		}
		sections = append(sections, peSection{
			vsize:   binary.LittleEndian.Uint32(secBuf[8:]),
			va:      binary.LittleEndian.Uint32(secBuf[12:]),
			rawSize: binary.LittleEndian.Uint32(secBuf[16:]),
			rawPtr:  binary.LittleEndian.Uint32(secBuf[20:]),
		})
	}
	if len(sections) == 0 {
		return nil, false
	}

	rvaToOff := func(rva uint32) (int64, bool) {
		for _, s := range sections {
			span := uint64(s.vsize)
			if span == 0 {
				span = uint64(s.rawSize)
			}
			if uint64(rva) < uint64(s.va) || uint64(rva) >= uint64(s.va)+span {
				continue
			}
			delta := uint64(rva - s.va)
			if delta >= uint64(s.rawSize) {
				continue
			}
			off := uint64(s.rawPtr) + delta
			if off >= uint64(size) {
				continue
			}
			return int64(off), true
		}
		return 0, false
	}

	// 资源目录内的偏移都相对于资源目录起始 RVA
	resOff := func(rel uint32) (int64, bool) {
		if uint64(resRVA)+uint64(rel) > 0xffffffff {
			return 0, false
		}
		return rvaToOff(resRVA + rel)
	}

	readDir := func(rel uint32) ([]resEntry, bool) {
		off, ok := resOff(rel)
		if !ok || off > size-16 {
			return nil, false
		}
		hdr := make([]byte, 16)
		if _, err := f.ReadAt(hdr, off); err != nil {
			return nil, false
		}
		count := int(binary.LittleEndian.Uint16(hdr[12:14])) + int(binary.LittleEndian.Uint16(hdr[14:16]))
		if count < 1 || count > 4096 {
			return nil, false
		}
		buf := make([]byte, count*8)
		if _, err := f.ReadAt(buf, off+16); err != nil {
			return nil, false
		}
		entries := make([]resEntry, 0, count)
		for i := 0; i < count; i++ {
			p := i * 8
			name := binary.LittleEndian.Uint32(buf[p:])
			target := binary.LittleEndian.Uint32(buf[p+4:])
			entries = append(entries, resEntry{
				id:     name & 0x7fffffff,
				off:    target & 0x7fffffff,
				named:  name&0x80000000 != 0,
				subdir: target&0x80000000 != 0,
			})
		}
		return entries, true
	}

	readData := func(rel uint32) ([]byte, bool) {
		off, ok := resOff(rel)
		if !ok || off > size-16 {
			return nil, false
		}
		ent := make([]byte, 16)
		if _, err := f.ReadAt(ent, off); err != nil {
			return nil, false
		}
		dataRVA := binary.LittleEndian.Uint32(ent[:4])
		dataSize := binary.LittleEndian.Uint32(ent[4:8])
		if dataSize == 0 || dataSize > maxIconEntryBytes {
			return nil, false
		}
		dataOff, ok := rvaToOff(dataRVA)
		if !ok || dataOff > size-int64(dataSize) {
			return nil, false
		}
		data := make([]byte, int(dataSize))
		if _, err := f.ReadAt(data, dataOff); err != nil {
			return nil, false
		}
		return data, true
	}

	// 资源树的层级在不同编译器下可能略有差异（有的省略语言层），递归找到第一个数据项
	var readFirstData func(rel uint32, depth int) ([]byte, bool)
	readFirstData = func(rel uint32, depth int) ([]byte, bool) {
		if depth <= 0 {
			return nil, false
		}
		entries, ok := readDir(rel)
		if !ok {
			return nil, false
		}
		for _, e := range entries {
			if e.subdir {
				if data, ok := readFirstData(e.off, depth-1); ok {
					return data, true
				}
			} else if data, ok := readData(e.off); ok {
				return data, true
			}
		}
		return nil, false
	}

	findType := func(entries []resEntry, id uint32) (uint32, bool) {
		for _, e := range entries {
			if !e.named && e.id == id && e.subdir {
				return e.off, true
			}
		}
		return 0, false
	}

	// 顶层资源类型：RT_GROUP_ICON(14) 与 RT_ICON(3)
	types, ok := readDir(0)
	if !ok {
		return nil, false
	}
	groupTypeOff, ok := findType(types, 14)
	if !ok {
		return nil, false
	}
	iconTypeOff, ok := findType(types, 3)
	if !ok {
		return nil, false
	}

	groups, ok := readDir(groupTypeOff)
	if !ok {
		return nil, false
	}
	icons, ok := readDir(iconTypeOff)
	if !ok {
		return nil, false
	}

	for _, group := range groups {
		var groupData []byte
		if group.subdir {
			groupData, ok = readFirstData(group.off, 3)
		} else {
			groupData, ok = readData(group.off)
		}
		if !ok || len(groupData) < 6 {
			continue
		}
		count := int(binary.LittleEndian.Uint16(groupData[4:6]))
		if count < 1 || count > 64 || len(groupData) < 6+count*14 {
			continue
		}

		// 选择最接近 48×48 的条目，兼顾清晰度和体积
		best, bestDiff := -1, 1<<30
		for i := 0; i < count; i++ {
			p := 6 + i*14
			w := int(groupData[p])
			if w == 0 {
				w = 256
			}
			if w < 16 {
				continue
			}
			if diff := absInt(w - 48); diff < bestDiff {
				best, bestDiff = i, diff
			}
		}
		if best < 0 {
			continue
		}

		p := 6 + best*14
		width := groupData[p]
		height := groupData[p+1]
		colorCount := groupData[p+2]
		planes := binary.LittleEndian.Uint16(groupData[p+4:])
		bitCount := binary.LittleEndian.Uint16(groupData[p+6:])
		iconID := binary.LittleEndian.Uint16(groupData[p+12:])

		var iconName *resEntry
		for i := range icons {
			if !icons[i].named && icons[i].id == uint32(iconID) {
				iconName = &icons[i]
				break
			}
		}
		if iconName == nil {
			continue
		}
		var iconData []byte
		if iconName.subdir {
			iconData, ok = readFirstData(iconName.off, 3)
		} else {
			iconData, ok = readData(iconName.off)
		}
		if !ok {
			continue
		}
		return buildICO(iconData, width, height, colorCount, planes, bitCount), true
	}
	return nil, false
}

// 将单个图标数据封装成最小 ICO 文件
func buildICO(data []byte, width, height, colorCount byte, planes, bitCount uint16) []byte {
	out := make([]byte, 22+len(data))
	binary.LittleEndian.PutUint16(out[2:4], 1) // type = icon
	binary.LittleEndian.PutUint16(out[4:6], 1) // count = 1
	out[6] = width
	out[7] = height
	out[8] = colorCount
	binary.LittleEndian.PutUint16(out[10:12], planes)
	binary.LittleEndian.PutUint16(out[12:14], bitCount)
	binary.LittleEndian.PutUint32(out[14:18], uint32(len(data)))
	binary.LittleEndian.PutUint32(out[18:22], 22)
	copy(out[22:], data)
	return out
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
