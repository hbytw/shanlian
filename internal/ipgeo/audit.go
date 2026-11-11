package ipgeo

import (
	"encoding/binary"
	"fmt"
	"math"
	"math/bits"
	"net"
	"sort"
	"strings"

	"github.com/lionsoul2014/ip2region/binding/golang/xdb"
)

// ---- IPv4 区间（闭区间） ----

// interval 是一个 IPv4 地址闭区间。
type interval struct {
	start, end uint32
}

func (iv interval) size() uint64 {
	return uint64(iv.end) - uint64(iv.start) + 1
}

// mergeIntervals 合并区间：重叠或相邻的合成一段。
func mergeIntervals(in []interval) []interval {
	if len(in) == 0 {
		return nil
	}
	sort.Slice(in, func(i, j int) bool {
		if in[i].start != in[j].start {
			return in[i].start < in[j].start
		}
		return in[i].end < in[j].end
	})
	out := []interval{in[0]}
	for _, iv := range in[1:] {
		last := &out[len(out)-1]
		if iv.start <= last.end {
			if iv.end > last.end {
				last.end = iv.end
			}
			continue
		}
		// 相邻也合并：1.0.0.0/24 与 1.0.1.0/24 本来就是连续的
		if last.end != math.MaxUint32 && iv.start == last.end+1 {
			last.end = iv.end
			continue
		}
		out = append(out, iv)
	}
	return out
}

// subtract 返回 a 中没有被 b 覆盖的部分。
//
// a、b 都必须是已排序且已合并的区间。
func subtract(a, b []interval) []interval {
	var out []interval
	for _, x := range a {
		cur := x
		for _, y := range b {
			if y.end < cur.start {
				continue
			}
			if y.start > cur.end {
				break
			}
			if y.start > cur.start {
				out = append(out, interval{cur.start, y.start - 1})
			}
			if y.end >= cur.end {
				// 整段被盖住：置成空区间（起点 > 终点）。
				// 别用 {0,0} —— 那是合法地址 0.0.0.0，
				// 而且空判断 start<=end 会把它当成有效区间吐出去，
				// 于是每次相减都多一个 0.0.0.0/32 的假网段。
				cur.start, cur.end = 1, 0
				break
			}
			cur.start = y.end + 1
		}
		if cur.start <= cur.end {
			out = append(out, cur)
		}
	}
	return out
}

// cidrToInterval 把 CIDR 字符串转成地址区间。
func cidrToInterval(s string) (interval, error) {
	_, ipnet, err := net.ParseCIDR(strings.TrimSpace(s))
	if err != nil {
		return interval{}, fmt.Errorf("解析 CIDR %q: %w", s, err)
	}
	ones, total := ipnet.Mask.Size()
	if total != 32 {
		return interval{}, fmt.Errorf("只支持 IPv4: %q", s)
	}
	v4 := ipnet.IP.To4()
	if v4 == nil {
		return interval{}, fmt.Errorf("只支持 IPv4: %q", s)
	}
	start := binary.BigEndian.Uint32(v4)
	mask := ^uint32(0) << (32 - ones)
	return interval{start: start & mask, end: start | ^mask}, nil
}

// rangeToCIDRs 把地址区间拆成 CIDR 列表。
func rangeToCIDRs(start, end uint32) []string {
	var out []string
	s, e := uint64(start), uint64(end)
	for s <= e {
		size := uint64(1)
		for s%(size*2) == 0 && s+size*2-1 <= e && size*2 <= 0x1_0000_0000 {
			size *= 2
		}
		prefix := 32 - (bits.Len64(size) - 1)
		out = append(out, fmt.Sprintf("%s/%d", u32ToIP(uint32(s)), prefix))
		s += size
	}
	return out
}

func u32ToIP(v uint32) string {
	return net.IPv4(byte(v>>24), byte(v>>16), byte(v>>8), byte(v)).String()
}

// ---- ip2region 库里的中国网段 ----

// CNRanges 是 ip2region 库中「中国大陆」网段的集合。
type CNRanges struct {
	intervals []interval
	segments  []cnSegment // 原始段（带归属地信息），按起点排序
	total     int         // 原始段数
	addresses uint64      // 覆盖的地址数
}

// cnSegment 是一段 CN 网段，连同它的归属地信息。
type cnSegment struct {
	iv     interval
	region Region
}

// Segments 返回原始段数。
func (r *CNRanges) Segments() int {
	if r == nil {
		return 0
	}
	return r.total
}

// Addresses 返回覆盖的地址数。
func (r *CNRanges) Addresses() uint64 {
	if r == nil {
		return 0
	}
	return r.addresses
}

// LoadCNRanges 从 xdb 文件枚举出中国大陆网段。
//
// 为什么要自己解析 xdb：官方 binding 只提供「按 IP 查」，
// 不提供「枚举全部段」。而交叉校验需要的恰恰是「ip2region 认为的 CN 段集合」——
// 只有拿到它，才能反向发现 cnlist 漏掉的段（境内被当成境外送进隧道）。
//
// 只支持 structure 3.0 + VectorIndex 索引策略，其它格式一律拒绝：
// 宁可报错，也不给出一份看起来正常但其实是错的结论。
func LoadCNRanges(path string) (*CNRanges, error) {
	if path == "" {
		return nil, fmt.Errorf("%w: 空路径", ErrNoDatabase)
	}
	content, err := xdb.LoadContentFromFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取 xdb %s: %w", path, err)
	}
	header, err := xdb.LoadHeaderFromBuff(content)
	if err != nil {
		return nil, fmt.Errorf("解析 xdb 头 %s: %w", path, err)
	}
	if header.IndexPolicy != xdb.VectorIndexPolicy {
		return nil, fmt.Errorf("xdb %s 用了不支持的索引策略 %s（只支持 VectorIndex）", path, header.IndexPolicy)
	}
	version, err := xdb.VersionFromHeader(header)
	if err != nil {
		return nil, fmt.Errorf("xdb %s 版本不支持: %w", path, err)
	}
	if version != xdb.IPv4 {
		return nil, fmt.Errorf("xdb %s 不是 IPv4 库（IPVersion=%d）", path, header.IPVersion)
	}

	segSize := xdb.IPv4.SegmentIndexSize
	vIndexLen := xdb.VectorIndexRows * xdb.VectorIndexCols * xdb.VectorIndexSize
	if len(content) < xdb.HeaderInfoLength+vIndexLen {
		return nil, fmt.Errorf("xdb %s 太短，结构不完整", path)
	}
	vIndex := content[xdb.HeaderInfoLength : xdb.HeaderInfoLength+vIndexLen]

	// 一级索引里每个槽给出它那段 segment index 的起止指针；
	// 槽与槽之间有重叠，合并后再逐段走，避免重复统计。
	var idxRanges []interval
	for i := 0; i+xdb.VectorIndexSize <= len(vIndex); i += xdb.VectorIndexSize {
		sPtr := binary.LittleEndian.Uint32(vIndex[i:])
		ePtr := binary.LittleEndian.Uint32(vIndex[i+4:])
		if sPtr == 0 || ePtr <= sPtr {
			continue
		}
		idxRanges = append(idxRanges, interval{sPtr, ePtr - 1})
	}
	idxRanges = mergeIntervals(idxRanges)

	var cn []interval
	var segs []cnSegment
	segments := 0
	segStride := uint32(segSize)
	for _, r := range idxRanges {
		for p := r.start; p+segStride <= r.end; p += segStride {
			off := int(p)
			startIP := binary.LittleEndian.Uint32(content[off : off+4])
			endIP := binary.LittleEndian.Uint32(content[off+4 : off+8])
			dataLen := binary.LittleEndian.Uint16(content[off+8 : off+10])
			dataPtr := binary.LittleEndian.Uint32(content[off+10 : off+14])
			if dataLen == 0 {
				continue
			}
			if int(dataPtr)+int(dataLen) > len(content) {
				continue
			}
			segments++
			region := parseRegion(string(content[int(dataPtr) : int(dataPtr)+int(dataLen)]))
			if !region.IsCN() {
				continue
			}
			if endIP < startIP {
				continue
			}
			iv := interval{start: startIP, end: endIP}
			cn = append(cn, iv)
			segs = append(segs, cnSegment{iv: iv, region: region})
		}
	}
	sort.Slice(segs, func(i, j int) bool { return segs[i].iv.start < segs[j].iv.start })
	cn = mergeIntervals(cn)
	var addr uint64
	for _, iv := range cn {
		addr += iv.size()
	}
	return &CNRanges{intervals: cn, segments: segs, total: segments, addresses: addr}, nil
}

// ---- 交叉校验 ----

// CNAudit 是「国内网段表 ↔ ip2region 归属地库」的交叉校验结果。
//
// 两个方向的含义不一样，报告里要分开讲：
//
//	漏段（cnlist 少收）：ip2region 说境内，cnlist 没覆盖。
//	    → 这类境内地址被当成境外送进隧道，白白消耗出口带宽并变慢。
//	错分（cnlist 多收）：cnlist 说境内，ip2region 不认。
//	    → 这类地址本该走隧道却走了直连，可能被墙或被污染，**更危险**。
//
// 但每个方向里都混着两种「不算错」的差异，不分类的报告会吓人：
//
//	口径差异：ip2region 把港澳台算作中国，cnlist 不算 —— 两边对「境内」的定义不同。
//	语义差异：cnlist 按「归属哪个运营商」判定，ip2region 按「地理位置」判定。
//	    运营商的国际出口段因此两边结论相反 —— cnlist 算境内，可流量实际从境外出去，
//	    对分流来说这类段恰恰**应该**走隧道。
//
// 分完类，剩下的才是真正需要人判断的「疑似漏段 / 疑似错分」。
type CNAudit struct {
	ListEntries    int
	ListValid      int
	ListAddresses  uint64
	GeoRanges      int
	GeoAddresses   uint64
	AgreeEntries   int
	PartialEntries int

	// 漏段方向：ip2region 说是境内、cnlist 没收
	MissingRanges  []string
	MissingAddrs   uint64
	MissingSamples []string
	// 口径差异：上面这些里属于港澳台的
	ScopeDiffRanges []string
	ScopeDiffAddrs  uint64

	// 错分方向：cnlist 收了、ip2region 不认
	FalsePositiveRanges  []string
	FalsePositiveAddrs   uint64
	FalsePositiveSamples []string
	// 语义差异：上面这些里属于中国运营商国际出口段的
	SemanticDiffRanges []string
	SemanticDiffAddrs  uint64

	InvalidEntries []string
}

// Clean 表示两边完全一致。
func (a *CNAudit) Clean() bool {
	return a != nil && len(a.FalsePositiveRanges) == 0 && len(a.MissingRanges) == 0
}

// 口径差异：港澳台在 ip2region 里算中国，cnlist 不算。
func isHKMT(r Region) bool {
	for _, k := range []string{"香港", "澳门", "台湾"} {
		if strings.Contains(r.Province, k) || strings.Contains(r.City, k) {
			return true
		}
	}
	return false
}

// 中国运营商在境外的出口段：cnlist 按运营商归属算境内，ip2region 按地理位置算境外。
var cnCarrierHints = []string{
	"电信", "联通", "移动", "网通", "铁通", "教育网",
	"China Telecom", "China Unicom", "China Mobile", "CERNET",
}

// isCNCarrier 判断归属地信息里是否指向中国运营商。
func isCNCarrier(r Region) bool {
	hay := r.ISP + " " + r.City + " " + r.Province
	for _, k := range cnCarrierHints {
		if strings.Contains(hay, k) {
			return true
		}
	}
	return false
}

// regionAt 返回覆盖 iv 的第一段 CN 网段的归属地。
func (r *CNRanges) regionAt(iv interval) Region {
	if r == nil || len(r.segments) == 0 {
		return Region{}
	}
	i := sort.Search(len(r.segments), func(i int) bool { return r.segments[i].iv.end >= iv.start })
	if i >= len(r.segments) || r.segments[i].iv.start > iv.end {
		return Region{}
	}
	return r.segments[i].region
}

// AuditCN 把一份国内网段表和 ip2region 的 CN 段集合对拍。
//
// lookup 可以为 nil；给了它才能把「语义差异」从「疑似错分」里分出来。
func AuditCN(cidrs []string, geo *CNRanges, lookup *Lookup) *CNAudit {
	res := &CNAudit{ListEntries: len(cidrs)}
	var list []interval
	for _, c := range cidrs {
		iv, err := cidrToInterval(c)
		if err != nil {
			res.InvalidEntries = append(res.InvalidEntries, c)
			continue
		}
		res.ListValid++
		list = append(list, iv)
	}
	for _, iv := range list {
		res.ListAddresses += iv.size()
	}
	list = mergeIntervals(list)

	var geoList []interval
	if geo != nil {
		geoList = geo.intervals
		res.GeoRanges = len(geoList)
		res.GeoAddresses = geo.Addresses()
	}

	// 条目级分类
	for _, c := range cidrs {
		iv, err := cidrToInterval(c)
		if err != nil {
			continue
		}
		switch overlapLevel(iv, geoList) {
		case overlapFull:
			res.AgreeEntries++
		case overlapPartial:
			res.PartialEntries++
		default:
			res.FalsePositiveSamples = append(res.FalsePositiveSamples, c)
		}
	}

	// 漏段方向：ip2region 有、cnlist 没有
	for _, iv := range subtract(geoList, list) {
		ranges := rangeToCIDRs(iv.start, iv.end)
		if isHKMT(geo.regionAt(iv)) {
			res.ScopeDiffAddrs += iv.size()
			res.ScopeDiffRanges = append(res.ScopeDiffRanges, ranges...)
			continue
		}
		res.MissingAddrs += iv.size()
		res.MissingRanges = append(res.MissingRanges, ranges...)
	}

	// 错分方向：cnlist 有、ip2region 不认
	for _, iv := range subtract(list, geoList) {
		ranges := rangeToCIDRs(iv.start, iv.end)
		if lookup != nil {
			if reg, err := lookup.Lookup(u32ToIP(iv.start)); err == nil && isCNCarrier(reg) {
				res.SemanticDiffAddrs += iv.size()
				res.SemanticDiffRanges = append(res.SemanticDiffRanges, ranges...)
				continue
			}
		}
		res.FalsePositiveAddrs += iv.size()
		res.FalsePositiveRanges = append(res.FalsePositiveRanges, ranges...)
	}
	return res
}

type overlapKind int

const (
	overlapNone overlapKind = iota
	overlapPartial
	overlapFull
)

// overlapLevel 判断 iv 与 geo 的重叠程度。
func overlapLevel(iv interval, geo []interval) overlapKind {
	if len(geo) == 0 {
		return overlapNone
	}
	// 二分找第一个可能相交的区间
	i := sort.Search(len(geo), func(i int) bool { return geo[i].end >= iv.start })
	if i >= len(geo) || geo[i].start > iv.end {
		return overlapNone
	}
	// geo 已合并，所以只需要看第一个相交区间与后续区间
	cur := iv
	for j := i; j < len(geo) && geo[j].start <= cur.end; j++ {
		if geo[j].start > cur.start || geo[j].end < cur.end {
			return overlapPartial
		}
		if geo[j].end >= cur.end {
			return overlapFull
		}
		cur.start = geo[j].end + 1
	}
	return overlapPartial
}
