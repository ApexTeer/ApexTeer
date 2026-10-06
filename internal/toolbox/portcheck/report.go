package portcheck

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/EasySB-Team/EasySB/internal/toolbox"
)

// Notes that are always part of a run: what was measured, how to read the
// states, and what this tool cannot see. They are constants because the wording
// is the report's argument, not decoration.
const (
	noteTarget        = "探测目标是本机公网 IP 的 TCP 端口（不是 127.0.0.1）：只有报文经过运营商/云厂商的边缘，才能看出该端口是否为上游放行。"
	noteStates        = "状态含义：「开放」= 三次握手完成；「可达(未监听)」= 对端回了 RST、报文已到本机，端口没有被上游拦截，只是没有程序在听；「超时」= 到点没有任何回应。"
	noteHairpin       = "从本机连自己的公网 IP 依赖 NAT 回环：不支持回环的云厂商会一律显示「超时」，此时无法区分「端口被拦」和「回环不通」，换一台机器复测可以确认。"
	noteIntercept     = "若本机有安全软件或透明代理接管了 TCP，任何端口都会秒连显示「开放」（连不可能应答的地址也秒连就是这个特征），那种「开放」不可信，应换一台机器复测。"
	noteOtherPorts    = "53/80/443 与邮局无关：80/443 供面板、订阅与 ACME HTTP-01 签发证书使用，53 供自建 DNS 使用。"
	notePort25Blocked = "25 端口不通：多数云厂商默认封锁 25（防垃圾邮件），需要提交工单申请解封；部分运营商的家宽 IP 无法解封。"
	noteLimits        = "本次只测入站可达性：出站 25 是否可用、IP 是否在主流黑名单里、本机防火墙是否放行这些端口，三样本工具都无法确认。"
)

// Verdict is the answer the tool exists to give.
type Verdict string

const (
	// VerdictYes means every requirement measured here is met: 25 is reachable
	// and the address has a PTR that resolves back to it.
	VerdictYes Verdict = "yes"
	// VerdictNo means at least one requirement is measurably unmet.
	VerdictNo Verdict = "no"
	// VerdictUnknown means the measurement did not run far enough to answer,
	// which is reported as such rather than as a hopeful yes.
	VerdictUnknown Verdict = "unknown"
)

// Text is the user-facing wording of the verdict.
func (v Verdict) Text() string {
	switch v {
	case VerdictYes:
		return "可以搭邮局"
	case VerdictNo:
		return "不建议搭邮局"
	default:
		return "无法确认"
	}
}

// Report is everything one run measured.
type Report struct {
	// Started and Elapsed are the run's timing, read from Options.Clock.
	Started time.Time
	Elapsed time.Duration
	// PublicIP is the address every probe dialled and the reverse lookup used.
	PublicIP string
	// IPSource names where PublicIP came from.
	IPSource string
	// Ports holds one result per catalogue entry, in catalogue order, whether or
	// not it was actually dialled.
	Ports []PortResult
	RDNS  RDNSResult
	// Verdict is the conclusion, and Notes carry the argument for it.
	Verdict Verdict
	Notes   []string
}

// Note appends one note line.
func (r *Report) Note(format string, args ...any) {
	r.Notes = append(r.Notes, fmt.Sprintf(format, args...))
}

// port returns the result for a port number; the zero value when it is not in
// the catalogue.
func (r Report) port(number int) PortResult {
	for _, p := range r.Ports {
		if p.Port.Number == number {
			return p
		}
	}
	return PortResult{Port: Port{Number: number}}
}

// decide writes the verdict and the notes that justify it. The rule is the one
// receivers apply: a mailbox needs 25 reachable from outside and a PTR that
// resolves back to the address. Anything this run could not read leaves the
// verdict at 无法确认 instead of guessing in either direction.
func (r *Report) decide() {
	mail := r.port(smtpPort)
	switch mail.Status {
	case StatusSkipped:
		r.Verdict = VerdictUnknown
		r.Note("无法确认：公网 IP 未取到，25 端口没有测。")
		return
	case StatusError:
		r.Verdict = VerdictUnknown
		r.Note("无法确认：25 端口探测出错（%s），既不是拒绝也不是超时，看不出是被拦还是回环不通。", mail.Error)
		return
	case StatusBlocked:
		r.Note(notePort25Blocked)
	}

	switch {
	case !mail.Status.Reachable():
		r.Verdict = VerdictNo
		r.Note("结论依据：25 端口不通（%s）→ 收不到别人的信，先解决 25 再谈 PTR。", mail.Status.Text())
	case r.RDNS.Failed:
		r.Verdict = VerdictUnknown
		r.Note("结论依据：25 端口可达，但 PTR 查询失败 → 反向解析这一半无法确认。")
	case r.RDNS.Missing:
		r.Verdict = VerdictNo
		r.Note("结论依据：25 端口可达，但 PTR 缺失 → 多数收信方会拒收，需要先到运营商/云厂商设置反向解析。")
	case !r.RDNS.ForwardConfirmed():
		r.Verdict = VerdictNo
		r.Note("结论依据：25 端口可达，PTR 存在但 FCrDNS 不一致 → 先把 PTR 的名字正解回本机 IP。")
	default:
		r.Verdict = VerdictYes
		r.Note("结论依据：25 端口可达 + PTR 存在且 FCrDNS 一致 → 可以搭邮局。")
	}
	r.Note(noteLimits)
}

// Summary is the one-line board text: the mail port, the reverse DNS, and the
// verdict, in that order.
func (r Report) Summary() string {
	if r.PublicIP == "" {
		return "邮件端口：无法确认 · 公网 IP 未取到"
	}
	parts := []string{"邮件端口：" + r.port(smtpPort).short()}
	switch {
	case r.RDNS.Failed:
		parts = append(parts, "PTR 无法确认")
	case r.RDNS.Missing:
		parts = append(parts, "PTR 缺失")
	case r.RDNS.ForwardConfirmed():
		parts = append(parts, "PTR 有 · FCrDNS 一致")
	default:
		parts = append(parts, "PTR 有 · FCrDNS 不一致")
	}
	return strings.Join(append(parts, r.verdictSegment()), " · ")
}

// verdictSegment is the verdict with the first thing standing in the way named,
// so the one line is actionable without opening the notes.
func (r Report) verdictSegment() string {
	switch r.Verdict {
	case VerdictYes, VerdictUnknown:
		return r.Verdict.Text()
	}
	switch mail := r.port(smtpPort); {
	case !mail.Status.Reachable():
		return "不建议搭邮局（25 需解封）"
	case r.RDNS.Missing:
		return "不建议搭邮局（缺 PTR）"
	default:
		return "不建议搭邮局（FCrDNS 不一致）"
	}
}

// Result renders the report as the toolbox table: one row per port, with the
// reverse DNS and the conclusion in the notes.
func (r Report) Result() toolbox.Result {
	out := toolbox.Result{
		Headers: []string{"端口", "用途", "状态"},
		Summary: r.Summary(),
	}
	for _, p := range r.Ports {
		out.Rows = append(out.Rows, []string{
			strconv.Itoa(p.Port.Number),
			p.Port.Purpose,
			p.Status.Text(),
		})
	}
	for _, p := range r.Ports {
		// A dial that failed for an unreadable reason is the one status whose
		// cell cannot carry its own explanation.
		if p.Status == StatusError && p.Error != "" {
			out.Note("端口 %d 拨号错误：%s", p.Port.Number, p.Error)
		}
	}
	out.Notes = append(out.Notes, r.Notes...)
	return out
}
