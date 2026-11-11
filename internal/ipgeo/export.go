package ipgeo

import "sort"

// ExportResult 是一次导出的结果。
type ExportResult struct {
	// CIDRs 是导出的网段（已排序、去重）。
	CIDRs []string
	// Segments 是 xdb 里参与导出的原始段数。
	Segments int
	// Addresses 是这些网段覆盖的地址数。
	Addresses uint64
	// HKMTExcluded 是被排除掉的港澳台段数。
	HKMTExcluded int
}

// ExportCNC 把 xdb 里的中国网段导出成 CIDR 列表。
//
// 用途：当在线源（clang.cn / APNIC）取不到、又没有任何本地缓存时，
// 这份**静态文件**导出的列表能顶上，让分流不至于整个不可用。
//
// includeHKMT 默认应传 false：ip2region 把港澳台算作中国，
// 而分流里港澳台通常**应该**走隧道，混进境内列表会让这些流量走直连。
func ExportCNC(xdbPath string, includeHKMT bool) (*ExportResult, error) {
	geo, err := LoadCNRanges(xdbPath)
	if err != nil {
		return nil, err
	}
	res := &ExportResult{}
	var ivs []interval
	for _, seg := range geo.segments {
		if !includeHKMT && isHKMT(seg.region) {
			res.HKMTExcluded++
			continue
		}
		res.Segments++
		ivs = append(ivs, seg.iv)
	}
	merged := mergeIntervals(ivs)
	seen := make(map[string]struct{}, len(merged)*4)
	for _, iv := range merged {
		res.Addresses += iv.size()
		for _, c := range rangeToCIDRs(iv.start, iv.end) {
			if _, dup := seen[c]; dup {
				continue
			}
			seen[c] = struct{}{}
			res.CIDRs = append(res.CIDRs, c)
		}
	}
	sort.Strings(res.CIDRs)
	return res, nil
}

// WriteCacheFormat 把 CIDR 列表写成 cnlist 缓存文件的格式。
//
// 格式与 cnlist 的缓存一致（首行元信息 + 每行一个 CIDR），
// 所以导出的文件可以直接当 cn.cache 用 —— 烤进镜像都行。
func WriteCacheFormat(cidrs []string, source string, updatedUnix int64) string {
	out := make([]byte, 0, len(cidrs)*16+64)
	out = append(out, "#slx-cn "...)
	out = append(out, itoa(updatedUnix)...)
	out = append(out, ' ')
	out = append(out, source...)
	out = append(out, '\n')
	for _, c := range cidrs {
		out = append(out, c...)
		out = append(out, '\n')
	}
	return string(out)
}

// itoa 把 int64 转成十进制字符串（不引 strconv 的一个小助手）。
func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
