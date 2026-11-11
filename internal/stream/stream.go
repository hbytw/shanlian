// Package stream 定义 proxy 模式下「一条多路复用流」的握手与中继。
//
// 一条流的生命周期：
//
//	网关                                 agent
//	 │  写 ConnectReq（长度前缀 JSON）      │
//	 │ ─────────────────────────────────→ │
//	 │                                    │  net.Dial(Addr:Port)
//	 │  读 ConnectResp                    │
//	 │ ←───────────────────────────────── │
//	 │                                    │
//	 │ ═════ 握手完成后即为纯数据管道 ═════ │
//
// 为什么握手要带长度前缀：流是字节流没有消息边界，直接写 JSON 无法知道读到哪为止。
// 长度前缀 + 单次 ReadFull 就能精确取出，且不需要引入额外依赖。
package stream

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"

	"slx/internal/proto"
)

// 握手消息的类型标记。取值不影响数据面：握手结束后流里就是裸数据。
const (
	KindConnectReq  uint8 = 1
	KindConnectResp uint8 = 2
	// KindProbeHello 网关 → agent：本流是探测通道（不走数据中继）。
	KindProbeHello uint8 = 3
	// KindPing / KindPong：探测通道上的往返采样，无载荷。
	KindPing uint8 = 4
	KindPong uint8 = 5
	// KindHostPing / KindHostPong：请求 agent 探测某主机可达性（ICMP echo）。
	KindHostPing uint8 = 6
	KindHostPong uint8 = 7
	// KindResolveReq / KindResolveResp：请 agent 用它那边的 DNS 解析域名。
	// 目的是拿到「贴着出口位置」的 CDN 地址，而不是网关当地解析出来的。
	KindResolveReq  uint8 = 8
	KindResolveResp uint8 = 9
	// KindAddrReport：agent 上报自己的公网地址与证书指纹。
	//
	// 走这条帧而不是改握手二进制格式：auth.go 的注释已经说明
	// 「加字段要动二进制帧格式并升协议版本，代价远大于收益」。
	// 而本包的帧机制（5 字节头 + JSON）本来就要求「不认识的消息要容忍」。
	//
	// 老 agent 不发 ⇒ 网关只有兜底通道 ⇒ 天然向后兼容。
	KindAddrReport uint8 = 10
)

// headerLen 是握手帧头：1 字节类型 + 4 字节大端长度。
const headerLen = 5

// MaxHandshake 是单条握手消息的最大长度。
//
// 4 KiB 远大于实际需要（ConnectReq 最大也就几百字节），
// 但它同时是「对端胡说八道时」的内存上限，不能省。
const MaxHandshake = 4096

// ErrHandshakeTooLarge 表示对端宣告的握手长度超限。
var ErrHandshakeTooLarge = errors.New("stream: 握手消息超长")

// writeMsg 写一条带长度前缀的握手消息。
func writeMsg(w io.Writer, kind uint8, v any) error {
	payload, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("stream: 握手序列化失败: %w", err)
	}
	if len(payload) > MaxHandshake {
		return ErrHandshakeTooLarge
	}
	var hdr [headerLen]byte
	hdr[0] = kind
	binary.BigEndian.PutUint32(hdr[1:], uint32(len(payload)))
	// 一次 Write 写完整帧：TCP/yamux 内部会保证不拆散到两条消息，
	// 但即便被拆成多次写也没关系，接收侧用的是 ReadFull。
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err = w.Write(payload)
	return err
}

// readMsg 读一条带长度前缀的握手消息。
func readMsg(r io.Reader) (uint8, []byte, error) {
	var hdr [headerLen]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(hdr[1:])
	if n > MaxHandshake {
		return 0, nil, ErrHandshakeTooLarge
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	return hdr[0], payload, nil
}

// WriteConnectReq 写建连请求。
func WriteConnectReq(w io.Writer, req proto.ConnectReq) error {
	return writeMsg(w, KindConnectReq, req)
}

// ReadConnectReq 读建连请求。
func ReadConnectReq(r io.Reader) (proto.ConnectReq, error) {
	var req proto.ConnectReq
	kind, payload, err := readMsg(r)
	if err != nil {
		return req, err
	}
	if kind != KindConnectReq {
		return req, fmt.Errorf("stream: 期望 connect_req，收到 kind=%d", kind)
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return req, fmt.Errorf("stream: connect_req 解析失败: %w", err)
	}
	return req, nil
}

// WriteConnectResp 写建连结果。
func WriteConnectResp(w io.Writer, resp proto.ConnectResp) error {
	return writeMsg(w, KindConnectResp, resp)
}

// ReadConnectResp 读建连结果。
func ReadConnectResp(r io.Reader) (proto.ConnectResp, error) {
	var resp proto.ConnectResp
	kind, payload, err := readMsg(r)
	if err != nil {
		return resp, err
	}
	if kind != KindConnectResp {
		return resp, fmt.Errorf("stream: 期望 connect_resp，收到 kind=%d", kind)
	}
	if err := json.Unmarshal(payload, &resp); err != nil {
		return resp, fmt.Errorf("stream: connect_resp 解析失败: %w", err)
	}
	return resp, nil
}

// RelayTCP 在两条字节流之间做双向拷贝，任一侧结束即关闭两侧。
//
// 返回值是「先结束的那一侧」的错误，用于日志；正常关闭（EOF）不算错误。
func RelayTCP(a, b net.Conn) error {
	done := make(chan error, 2)
	cp := func(dst, src net.Conn) {
		_, err := io.Copy(dst, src)
		// 半关闭：把 EOF 传给对端，但对端可能还想继续发（HTTP/1.1 流水线等）。
		// 这里选择双向都关，简单可预测；需要半关闭语义的场景走 packet 模式。
		_ = dst.Close()
		_ = src.Close()
		done <- err
	}
	go cp(a, b)
	go cp(b, a)
	err := <-done
	if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

// relayUDPDatagramMax 是 UDP 数据报的最大长度（含 IP 头之后的最大载荷）。
const relayUDPDatagramMax = 65535

// UDP 帧类型：数据 / 远端错误。
const (
	udpFrameData  uint8 = 0
	udpFrameError uint8 = 1
)

// UDP 错误码，用于把 agent 侧的 socket 错误传回网关。
const (
	UDPErrNone uint8 = iota
	UDPErrPortUnreachable
)

// udpFrameHeaderLen 是 UDP 帧头：1 字节类型 + 1 字节错误码 + 4 字节长度。
const udpFrameHeaderLen = 6

// FramedPipe 在流与 UDP socket 之间中继数据报，保留数据报边界。
//
// 为什么不能直接 io.Copy：UDP 是数据报语义，字节流拼接会把两个数据报粘成一个，
// 应用层解析直接错位。必须每条数据报独立成帧。
//
// # 结束时两侧都关
//
// 帧格式管道退出时会把 **socket 与隧道都关掉**：
// 隧道不关的话，每条 UDP 四元组都会永久泄漏一条 yamux 流
// （审计报告「根因 B3」）。隧道实现了 io.Closer 才关（两端实际都是 Closer）。
//
// # 只用于 UDP
//
// TCP 走的是 RelayTCP（字节流语义），**不经过本函数**
// ⇒ 这里的改动不会影响 TCP 路径的行为。
//
// classifyErr 把本地 socket 错误翻译成线协议错误码（这样网关知道该不该回
// ICMP port unreachable）；onRemoteErr 在收到对端错误帧时回调，用于让网关
// 把错误投给对应的 UDP 端点。
func FramedPipe(
	tunnel io.ReadWriter,
	sock net.Conn,
	classifyErr func(error) uint8,
) error {
	stop := make(chan error, 2)

	// socket → tunnel：打数据帧
	go func() {
		buf := make([]byte, relayUDPDatagramMax)
		for {
			n, err := sock.Read(buf)
			if n > 0 {
				if werr := writeUDPFrame(tunnel, udpFrameData, UDPErrNone, buf[:n]); werr != nil {
					stop <- werr
					return
				}
			}
			if err != nil {
				if classifyErr != nil {
					if code := classifyErr(err); code != UDPErrNone {
						_ = writeUDPFrame(tunnel, udpFrameError, code, nil)
					}
				}
				stop <- err
				return
			}
		}
	}()

	// tunnel → socket：拆帧
	go func() {
		hdr := make([]byte, udpFrameHeaderLen)
		for {
			if _, err := io.ReadFull(tunnel, hdr); err != nil {
				stop <- err
				return
			}
			n := binary.BigEndian.Uint32(hdr[2:])
			if n > relayUDPDatagramMax {
				stop <- fmt.Errorf("stream: UDP 帧长度非法: %d", n)
				return
			}
			payload := make([]byte, n)
			if _, err := io.ReadFull(tunnel, payload); err != nil {
				stop <- err
				return
			}
			switch hdr[0] {
			case udpFrameData:
				if _, err := sock.Write(payload); err != nil {
					stop <- err
					return
				}
			case udpFrameError:
				stop <- fmt.Errorf("stream: 对端 UDP socket 错误 code=%d", hdr[1])
				return
			default:
				stop <- fmt.Errorf("stream: 未知 UDP 帧类型 %d", hdr[0])
				return
			}
		}
	}()

	err := <-stop
	_ = sock.Close()
	// **必须把隧道也关掉。**
	//
	// 为什么：FramedPipe 原来只关 socket，隧道（yamux 流）永远不关
	// ⇒ 每条 UDP 四元组永久泄漏一条 yamux 流 + 一个 goroutine
	// （网关侧一份、agent 侧一份，审计报告「根因 B3」）。
	// UDP 是无连接的、churn 极高 ⇒ 泄漏会持续累积。
	//
	// 为什么用类型断言：tunnel 的静态类型是 io.ReadWriter，
	// 不保证实现了 io.Closer。实际上两端传进来的都是 Closer：
	// 网关侧是 net.Conn（yamux 流），agent 侧是 *yamux.Stream。
	if c, ok := tunnel.(io.Closer); ok {
		_ = c.Close()
	}
	return err
}

// writeUDPFrame 写一条 UDP 帧。
func writeUDPFrame(w io.Writer, kind, code uint8, payload []byte) error {
	var hdr [udpFrameHeaderLen]byte
	hdr[0] = kind
	hdr[1] = code
	binary.BigEndian.PutUint32(hdr[2:], uint32(len(payload)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	if len(payload) == 0 {
		return nil
	}
	_, err := w.Write(payload)
	return err
}

// ClassifyNetError 把 dial/read 错误归类成 UDP 错误码。
//
// 只区分「端口不可达」和其它：前者网关需要回 ICMP port unreachable，
// 否则应用会一直等（表现为 UDP 应用挂住）。
func ClassifyNetError(err error) uint8 {
	if err == nil {
		return UDPErrNone
	}
	msg := err.Error()
	// 跨平台错误串匹配：Go 的 syscall 错误在 Linux 上是 "connection refused"，
	// Windows 上是 "No connection could be made because the target machine actively refused it"。
	for _, s := range []string{"connection refused", "actively refused"} {
		if containsFold(msg, s) {
			return UDPErrPortUnreachable
		}
	}
	return UDPErrNone
}

// containsFold 是大小写无关的子串判断（避免为一个小工具引入 strings 依赖差异）。
func containsFold(s, sub string) bool {
	if len(sub) > len(s) {
		return false
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		match := true
		for j := 0; j < len(sub); j++ {
			a, b := s[i+j], sub[j]
			if 'A' <= a && a <= 'Z' {
				a += 'a' - 'A'
			}
			if 'A' <= b && b <= 'Z' {
				b += 'a' - 'A'
			}
			if a != b {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// ReadFrame 读一个帧，返回类型与载荷。
//
// 探测通道只需要「类型」；数据通道用具体类型的解码函数。
// 统一走这一个入口，避免每种帧各写一套头部解析。
func ReadFrame(r io.Reader) (uint8, []byte, error) {
	return readMsg(r)
}

// DecodeConnectReq 把已读出的载荷解码成建连请求。
func DecodeConnectReq(payload []byte) (proto.ConnectReq, error) {
	var req proto.ConnectReq
	if err := json.Unmarshal(payload, &req); err != nil {
		return req, fmt.Errorf("stream: connect_req 解析失败: %w", err)
	}
	return req, nil
}

// WriteControlFrame 写一个无载荷的控制帧（ProbeHello / Ping / Pong）。
//
// 直接写 5 字节头、长度 0，不走 JSON：探测每 2 秒一次、每条 agent 一次，
// 这条路径要尽量便宜。
func WriteControlFrame(w io.Writer, kind uint8) error {
	var hdr [headerLen]byte
	hdr[0] = kind
	binary.BigEndian.PutUint32(hdr[1:], 0)
	_, err := w.Write(hdr[:])
	return err
}

// WriteHostPing 写一条主机可达性探测请求。
func WriteHostPing(w io.Writer, req proto.HostPingReq) error {
	return writeMsg(w, KindHostPing, req)
}

// DecodeHostPing 解码主机可达性探测请求载荷。
func DecodeHostPing(payload []byte) (proto.HostPingReq, error) {
	var req proto.HostPingReq
	if err := json.Unmarshal(payload, &req); err != nil {
		return req, fmt.Errorf("stream: host_ping 解析失败: %w", err)
	}
	return req, nil
}

// WriteHostPong 写一条主机可达性探测结果。
func WriteHostPong(w io.Writer, resp proto.HostPingResp) error {
	return writeMsg(w, KindHostPong, resp)
}

// DecodeHostPong 解码主机可达性探测结果载荷。
func DecodeHostPong(payload []byte) (proto.HostPingResp, error) {
	var resp proto.HostPingResp
	if err := json.Unmarshal(payload, &resp); err != nil {
		return resp, fmt.Errorf("stream: host_pong 解析失败: %w", err)
	}
	return resp, nil
}

// WriteResolveReq 写一条「请帮我解析这个域名」请求。
func WriteResolveReq(w io.Writer, req proto.ResolveReq) error {
	return writeMsg(w, KindResolveReq, req)
}

// DecodeResolveReq 解码解析请求载荷。
func DecodeResolveReq(payload []byte) (proto.ResolveReq, error) {
	var req proto.ResolveReq
	if err := json.Unmarshal(payload, &req); err != nil {
		return req, fmt.Errorf("stream: resolve_req 解析失败: %w", err)
	}
	return req, nil
}

// WriteResolveResp 写解析结果。
func WriteResolveResp(w io.Writer, resp proto.ResolveResp) error {
	return writeMsg(w, KindResolveResp, resp)
}

// DecodeResolveResp 解码解析结果载荷。
func DecodeResolveResp(payload []byte) (proto.ResolveResp, error) {
	var resp proto.ResolveResp
	if err := json.Unmarshal(payload, &resp); err != nil {
		return resp, fmt.Errorf("stream: resolve_resp 解析失败: %w", err)
	}
	return resp, nil
}

// WriteAddrReport 写一条地址上报。
//
// 用现有的 writeMsg（JSON 载荷）而不是自定义二进制：
// 这条消息每条 agent 只在连接建立时发一次（以及证书续期后一次），
// 体积无关紧要；用 JSON 的好处是可读、可调试、向前兼容。
func WriteAddrReport(w io.Writer, rep proto.AddrReport) error {
	return writeMsg(w, KindAddrReport, rep)
}

// DecodeAddrReport 解码地址上报载荷。
func DecodeAddrReport(payload []byte) (proto.AddrReport, error) {
	var rep proto.AddrReport
	if err := json.Unmarshal(payload, &rep); err != nil {
		return rep, fmt.Errorf("stream: addr_report 解析失败: %w", err)
	}
	return rep, nil
}
