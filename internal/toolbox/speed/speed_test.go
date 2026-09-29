package speed

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MinimaxFlora/EasySB/internal/toolbox"
)

// stubRunner answers the runner calls from tables, so no test in this file opens a
// socket.
type stubRunner struct {
	list    []server
	listErr error
	samples map[string]sample
	errs    map[string]error
	// asked records the ids the run measured, in order.
	asked []string
}

func (s *stubRunner) servers(context.Context) ([]server, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.list, nil
}

func (s *stubRunner) measure(_ context.Context, srv server) (sample, error) {
	s.asked = append(s.asked, srv.id)
	if err := s.errs[srv.id]; err != nil {
		return sample{}, err
	}
	return s.samples[srv.id], nil
}

// sampleAt builds a reading in the units the runner reports: latency in milliseconds,
// rates in Mbps (the table's unit).
func sampleAt(latencyMs int, downMbps, upMbps float64) sample {
	return sample{
		latency:  time.Duration(latencyMs) * time.Millisecond,
		download: rate(downMbps * 1e6),
		upload:   rate(upMbps * 1e6),
	}
}

func containsNote(notes []string, want string) bool {
	for _, note := range notes {
		if strings.Contains(note, want) {
			return true
		}
	}
	return false
}

// nearbyList is six servers, nearest first, so a run has something to leave out.
func nearbyList() []server {
	return []server{
		{id: "1", name: "静冈", sponsor: "sudosa", distance: 9.03},
		{id: "2", name: "富士", sponsor: "AT2", distance: 120.55},
		{id: "3", name: "横滨", sponsor: "at2wn", distance: 125.44},
		{id: "4", name: "东京", sponsor: "Cordeos", distance: 148.23},
		{id: "5", name: "大阪", sponsor: "Optage", distance: 400.10},
		{id: "6", name: "福冈", sponsor: "QTnet", distance: 900.00},
	}
}

func nearbySamples() map[string]sample {
	return map[string]sample{
		"1": sampleAt(10, 320, 95),
		"2": sampleAt(20, 200, 80),
		"3": sampleAt(30, 100, 40),
		"4": sampleAt(40, 80, 30),
		"5": sampleAt(15, 500, 300),
		"6": sampleAt(25, 600, 400),
	}
}

func TestNearby(t *testing.T) {
	tests := []struct {
		name        string
		runner      func() *stubRunner
		wantRows    [][]string
		wantNotes   []string
		absentNotes []string
		wantSummary string
		wantAsked   []string
		wantErr     bool
	}{
		{
			name:   "the nearest four are measured, the rest untouched",
			runner: func() *stubRunner { return &stubRunner{list: nearbyList(), samples: nearbySamples()} },
			wantRows: [][]string{
				{"静冈", "sudosa", "10 ms", "320.00 Mbps", "95.00 Mbps"},
				{"富士", "AT2", "20 ms", "200.00 Mbps", "80.00 Mbps"},
				{"横滨", "at2wn", "30 ms", "100.00 Mbps", "40.00 Mbps"},
				{"东京", "Cordeos", "40 ms", "80.00 Mbps", "30.00 Mbps"},
				{"总计（均值）", "", "25 ms", "175.00 Mbps", "61.25 Mbps"},
			},
			wantNotes: []string{
				"数据来源：speedtest.net 公共服务器列表",
				"选取规则：本机最近 4 个节点",
				"已选节点距离本机 9.03 km ～ 148.23 km",
				"总计为已测得节点的算术平均",
			},
			absentNotes: []string{"本次跳过", "测速被中断"},
			wantSummary: "就近测速：下行 175.00 Mbps · 上行 61.25 Mbps · 4 节点",
			wantAsked:   []string{"1", "2", "3", "4"},
		},
		{
			name: "a failing node is skipped and the total covers what answered",
			runner: func() *stubRunner {
				return &stubRunner{
					list:    nearbyList(),
					samples: nearbySamples(),
					errs:    map[string]error{"2": errors.New("下行测量失败：连接被重置")},
				}
			},
			wantRows: [][]string{
				{"静冈", "sudosa", "10 ms", "320.00 Mbps", "95.00 Mbps"},
				{"横滨", "at2wn", "30 ms", "100.00 Mbps", "40.00 Mbps"},
				{"东京", "Cordeos", "40 ms", "80.00 Mbps", "30.00 Mbps"},
				{"总计（均值）", "", "27 ms", "166.67 Mbps", "55.00 Mbps"},
			},
			wantNotes: []string{
				"跳过 2 富士：下行测量失败：连接被重置。",
				"本次跳过 1 个测速失败的节点。",
			},
			wantSummary: "就近测速：下行 166.67 Mbps · 上行 55.00 Mbps · 3 节点",
			wantAsked:   []string{"1", "2", "3", "4"},
		},
		{
			name:     "every node fails",
			runner:   func() *stubRunner { return &stubRunner{list: nearbyList(), errs: everyNodeFailed()} },
			wantRows: nil,
			wantNotes: []string{
				"跳过 1 静冈",
				"跳过 4 东京",
				"数据来源：speedtest.net 公共服务器列表",
			},
			wantSummary: "就近测速：4 个节点全部测速失败",
			wantAsked:   []string{"1", "2", "3", "4"},
		},
		{
			name:        "an empty server list is a note, not a failure",
			runner:      func() *stubRunner { return &stubRunner{} },
			wantRows:    nil,
			wantNotes:   []string{"数据来源：speedtest.net 公共服务器列表", "服务器列表为空，没有可测节点"},
			wantSummary: "就近测速：没有可测节点",
		},
		{
			name:    "a list that cannot be fetched fails the run",
			runner:  func() *stubRunner { return &stubRunner{listErr: errors.New("dial tcp: lookup www.speedtest.net")} },
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := tt.runner()
			res, err := nearby(context.Background(), toolbox.Options{}, r)
			if tt.wantErr {
				if err == nil {
					t.Fatal("nearby() returned no error for an unfetchable list")
				}
				if res.Headers != nil || res.Rows != nil || res.Summary != "" {
					t.Errorf("failed run returned a result: %+v", res)
				}
				return
			}
			if err != nil {
				t.Fatalf("nearby() returned an error: %v", err)
			}
			if !reflect.DeepEqual(res.Headers, []string{"节点", "提供商", "延迟", "下行", "上行"}) {
				t.Errorf("headers = %v", res.Headers)
			}
			if !reflect.DeepEqual(res.Rows, tt.wantRows) {
				t.Errorf("rows = %v, want %v", res.Rows, tt.wantRows)
			}
			for _, want := range tt.wantNotes {
				if !containsNote(res.Notes, want) {
					t.Errorf("notes %v do not contain %q", res.Notes, want)
				}
			}
			for _, absent := range tt.absentNotes {
				if containsNote(res.Notes, absent) {
					t.Errorf("notes %v contain %q, which does not belong there", res.Notes, absent)
				}
			}
			if res.Summary != tt.wantSummary {
				t.Errorf("summary = %q, want %q", res.Summary, tt.wantSummary)
			}
			if !reflect.DeepEqual(r.asked, tt.wantAsked) {
				t.Errorf("runner was asked for %v, want %v", r.asked, tt.wantAsked)
			}
		})
	}
}

// everyNodeFailed is the error table for the four servers a run selects.
func everyNodeFailed() map[string]error {
	return map[string]error{
		"1": errors.New("延迟测量失败：i/o timeout"),
		"2": errors.New("延迟测量失败：i/o timeout"),
		"3": errors.New("延迟测量失败：i/o timeout"),
		"4": errors.New("延迟测量失败：i/o timeout"),
	}
}

func TestCarriers(t *testing.T) {
	// Two 电信 servers so the choice has to take the nearer one, plus one foreign node
	// that must never be tested by this entry.
	list := []server{
		{id: "11", name: "广州", sponsor: "中国电信 Guangdong", distance: 40},
		{id: "12", name: "深圳", sponsor: "中国电信 Shenzhen", distance: 15},
		{id: "21", name: "北京", sponsor: "中国联通 Beijing", distance: 900},
		{id: "31", name: "上海", sponsor: "中国移动 Shanghai", distance: 120},
		{id: "41", name: "东京", sponsor: "Softbank", distance: 5},
	}
	samples := map[string]sample{
		"12": sampleAt(10, 300, 100),
		"21": sampleAt(20, 200, 50),
		"31": sampleAt(30, 100, 25),
	}
	tests := []struct {
		name        string
		runner      func() *stubRunner
		wantRows    [][]string
		wantNotes   []string
		absentNotes []string
		wantSummary string
		wantAsked   []string
	}{
		{
			name:   "one server per carrier, the nearest of each",
			runner: func() *stubRunner { return &stubRunner{list: list, samples: samples} },
			wantRows: [][]string{
				{"深圳", "中国电信 Shenzhen", "10 ms", "300.00 Mbps", "100.00 Mbps"},
				{"北京", "中国联通 Beijing", "20 ms", "200.00 Mbps", "50.00 Mbps"},
				{"上海", "中国移动 Shanghai", "30 ms", "100.00 Mbps", "25.00 Mbps"},
				{"总计（均值）", "", "20 ms", "200.00 Mbps", "58.33 Mbps"},
			},
			wantNotes: []string{
				"选取规则：节点名或提供商包含 电信 / 联通 / 移动",
				"本次 电信 1 · 联通 1 · 移动 1。",
			},
			absentNotes: []string{"未匹配到", "本次跳过", "测速被中断"},
			wantSummary: "三网测速：下行 200.00 Mbps · 上行 58.33 Mbps · 3 节点",
			wantAsked:   []string{"12", "21", "31"},
		},
		{
			name: "a carrier the list does not carry is reported missing",
			runner: func() *stubRunner {
				kept := []server{list[0], list[1], list[2], list[4]}
				return &stubRunner{list: kept, samples: samples}
			},
			wantRows: [][]string{
				{"深圳", "中国电信 Shenzhen", "10 ms", "300.00 Mbps", "100.00 Mbps"},
				{"北京", "中国联通 Beijing", "20 ms", "200.00 Mbps", "50.00 Mbps"},
				{"总计（均值）", "", "15 ms", "250.00 Mbps", "75.00 Mbps"},
			},
			wantNotes: []string{
				"本次 电信 1 · 联通 1。",
				"未匹配到 移动 节点",
			},
			wantSummary: "三网测速：下行 250.00 Mbps · 上行 75.00 Mbps · 2 节点",
			wantAsked:   []string{"12", "21"},
		},
		{
			name: "a failing carrier node is skipped, the others still report",
			runner: func() *stubRunner {
				return &stubRunner{list: list, samples: samples, errs: map[string]error{"21": errors.New("延迟测量失败：i/o timeout")}}
			},
			wantRows: [][]string{
				{"深圳", "中国电信 Shenzhen", "10 ms", "300.00 Mbps", "100.00 Mbps"},
				{"上海", "中国移动 Shanghai", "30 ms", "100.00 Mbps", "25.00 Mbps"},
				{"总计（均值）", "", "20 ms", "200.00 Mbps", "62.50 Mbps"},
			},
			wantNotes:   []string{"跳过 21 北京：延迟测量失败：i/o timeout。"},
			wantSummary: "三网测速：下行 200.00 Mbps · 上行 62.50 Mbps · 2 节点",
			wantAsked:   []string{"12", "21", "31"},
		},
		{
			name: "no carrier node: an empty table and a note, never a substitute",
			runner: func() *stubRunner {
				return &stubRunner{list: []server{
					{id: "jp", name: "东京", sponsor: "Softbank", distance: 5},
					{id: "kr", name: "首尔", sponsor: "Korea Telecom", distance: 800},
					{id: "sg", name: "新加坡", sponsor: "Singtel", distance: 4000},
				}}
			},
			wantRows: nil,
			wantNotes: []string{
				"没有匹配到三网节点：列表里的 3 个节点",
				"数据来源：speedtest.net 公共服务器列表",
			},
			wantSummary: "三网测速：没有匹配到三网节点",
		},
		{
			name:        "an empty server list",
			runner:      func() *stubRunner { return &stubRunner{} },
			wantRows:    nil,
			wantNotes:   []string{"没有匹配到三网节点：speedtest.net 返回的服务器列表为空"},
			wantSummary: "三网测速：没有匹配到三网节点",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := tt.runner()
			res, err := carriers(context.Background(), toolbox.Options{}, r)
			if err != nil {
				t.Fatalf("carriers() returned an error: %v", err)
			}
			if !reflect.DeepEqual(res.Rows, tt.wantRows) {
				t.Errorf("rows = %v, want %v", res.Rows, tt.wantRows)
			}
			for _, want := range tt.wantNotes {
				if !containsNote(res.Notes, want) {
					t.Errorf("notes %v do not contain %q", res.Notes, want)
				}
			}
			for _, absent := range tt.absentNotes {
				if containsNote(res.Notes, absent) {
					t.Errorf("notes %v contain %q, which does not belong there", res.Notes, absent)
				}
			}
			if res.Summary != tt.wantSummary {
				t.Errorf("summary = %q, want %q", res.Summary, tt.wantSummary)
			}
			if !reflect.DeepEqual(r.asked, tt.wantAsked) {
				t.Errorf("runner was asked for %v, want %v", r.asked, tt.wantAsked)
			}
		})
	}
}

// blockingRunner never answers: it waits for the per-node deadline.
type blockingRunner struct {
	stubRunner
}

func (b *blockingRunner) measure(ctx context.Context, _ server) (sample, error) {
	<-ctx.Done()
	return sample{}, ctx.Err()
}

func TestNearbyNodeTimeout(t *testing.T) {
	// Two nodes share the 1.1 s budget, so the first one runs out of its own 550 ms slice
	// while the run itself still has time left: that is what the per-node note is for.
	list := []server{
		{id: "1", name: "慢节点", distance: 1},
		{id: "2", name: "慢节点", distance: 2},
	}
	r := &blockingRunner{stubRunner{list: list}}
	res, err := nearby(context.Background(), toolbox.Options{Timeout: 1100 * time.Millisecond}, r)
	if err != nil {
		t.Fatalf("nearby() returned an error: %v", err)
	}
	if len(res.Rows) != 0 {
		t.Errorf("rows = %v, want none", res.Rows)
	}
	if !containsNote(res.Notes, "跳过 1 慢节点：超过单节点时限") {
		t.Errorf("notes %v do not name the per-node deadline", res.Notes)
	}
	if !containsNote(res.Notes, "已用完本次总时限") {
		t.Errorf("notes %v do not name the run budget", res.Notes)
	}
	if res.Summary != "就近测速：已用完总时限，未测得节点" {
		t.Errorf("summary = %q", res.Summary)
	}
}

// delayedRunner answers every node after a fixed wait, ignoring its deadline the way a
// server that is merely slow would: the run then has to stop on its own budget.
type delayedRunner struct {
	stubRunner
	delay time.Duration
}

func (d *delayedRunner) measure(_ context.Context, srv server) (sample, error) {
	time.Sleep(d.delay)
	d.asked = append(d.asked, srv.id)
	return sampleAt(10, 100, 50), nil
}

func TestNearbyStopsWhenBudgetSpent(t *testing.T) {
	// The run checks its budget before starting each node, so a node's turn comes at
	// (nodes asked) x delay. These numbers put every check far from the deadline: the
	// first starts at 0 ms against a 300 ms budget, and the second would start at
	// 500 ms, 200 ms past it.
	//
	// Do not tighten this. Four nodes at 100 ms against a 300 ms budget - the shape this
	// test used to have - puts the last check exactly on the deadline, so the node count
	// came down to whether three 100 ms sleeps summed to just under or just over 300 ms.
	// It failed about one run in five and passed under load, which is the opposite of
	// what the comment claimed and enough to fail the release gate at random.
	const (
		delay  = 500 * time.Millisecond
		budget = 300 * time.Millisecond
	)
	list := make([]server, 0, 4)
	for i := 1; i <= 4; i++ {
		list = append(list, server{id: strconv.Itoa(i), name: "慢节点", distance: float64(i)})
	}
	r := &delayedRunner{stubRunner: stubRunner{list: list}, delay: delay}
	started := time.Now()
	res, err := nearby(context.Background(), toolbox.Options{Timeout: budget}, r)
	if err != nil {
		t.Fatalf("nearby() returned an error: %v", err)
	}
	if !containsNote(res.Notes, "已用完本次总时限") {
		t.Errorf("notes %v do not name the spent budget", res.Notes)
	}
	if !strings.HasSuffix(res.Summary, "（超时）") {
		t.Errorf("summary = %q, want a timeout marker", res.Summary)
	}
	// Exactly the first node ran: the run stopped at the budget rather than working
	// through the list, and the reading it did take is still reported.
	if len(r.asked) != 1 {
		t.Errorf("asked %v, want only the first node", r.asked)
	}
	if len(res.Rows) == 0 {
		t.Error("the one node that was measured produced no row")
	}
	// A guard against the run ignoring its budget altogether: one delay is all this is
	// allowed to cost.
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Errorf("the run took %s, so it did not stop at the budget", elapsed)
	}
}

func TestNearbyStopsWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := &stubRunner{list: nearbyList(), samples: nearbySamples()}
	res, err := nearby(ctx, toolbox.Options{}, r)
	if err != nil {
		t.Fatalf("nearby() returned an error: %v", err)
	}
	if len(r.asked) != 0 {
		t.Errorf("a cancelled run still measured %v", r.asked)
	}
	if !containsNote(res.Notes, "测速被中断") {
		t.Errorf("notes %v do not mention the interruption", res.Notes)
	}
	if res.Summary != "就近测速：测速已中断，未测得节点" {
		t.Errorf("summary = %q", res.Summary)
	}
}

func TestTools(t *testing.T) {
	tools := Tools()
	if len(tools) != 2 {
		t.Fatalf("Tools() returned %d entries, want 2", len(tools))
	}
	want := []string{IDNearby, IDCarriers}
	for i, tool := range tools {
		if tool.ID != want[i] {
			t.Errorf("entry %d has id %q, want %q", i, tool.ID, want[i])
		}
		if tool.Group != Group {
			t.Errorf("%s is filed under %q, want %q", tool.ID, tool.Group, Group)
		}
		if tool.Run == nil {
			t.Errorf("%s has no Run", tool.ID)
		}
	}
}

// TestRateMbps pins the unit conversion: the runner reports bits per second and the
// table prints Mbps.
func TestRateMbps(t *testing.T) {
	tests := []struct {
		name    string
		bits    rate
		wantMps string
	}{
		{name: "one megabit", bits: rate(1_000_000), wantMps: "1.00"},
		{name: "a fast line", bits: rate(320_420_000), wantMps: "320.42"},
		{name: "zero stays zero", bits: rate(0), wantMps: "0.00"},
		{
			name:    "speedtest-go reports bytes per second",
			bits:    bitsPerSecond(125000), // 125000 B/s is exactly 1 Mbps
			wantMps: "1.00",
		},
		{
			name:    "speedtest-go reading converted",
			bits:    bitsPerSecond(40_000_000), // 40 MB/s
			wantMps: "320.00",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := strconv.FormatFloat(tt.bits.mbps(), 'f', 2, 64); got != tt.wantMps {
				t.Errorf("mbps() = %s Mbps, want %s Mbps", got, tt.wantMps)
			}
			if want := tt.wantMps + " Mbps"; tt.bits.String() != want {
				t.Errorf("String() = %q, want %q", tt.bits.String(), want)
			}
		})
	}
}

func TestLatencyCell(t *testing.T) {
	tests := []struct {
		name string
		d    time.Duration
		want string
	}{
		{name: "whole milliseconds", d: 12 * time.Millisecond, want: "12 ms"},
		{name: "rounds up", d: 1500 * time.Microsecond, want: "2 ms"},
		{name: "rounds down", d: 1400 * time.Microsecond, want: "1 ms"},
		{name: "sub-millisecond", d: 300 * time.Microsecond, want: "0 ms"},
		{name: "zero", d: 0, want: "0 ms"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := latencyCell(tt.d); got != tt.want {
				t.Errorf("latencyCell(%s) = %q, want %q", tt.d, got, tt.want)
			}
		})
	}
}

// TestSampleValid covers the rule that keeps a non-measurement out of the table:
// speedtest-go's -1 ("N/A") and a zero counter are errors, not speeds.
func TestSampleValid(t *testing.T) {
	good := sampleAt(10, 320, 95)
	tests := []struct {
		name    string
		sample  sample
		wantErr bool
	}{
		{name: "measured", sample: good},
		{name: "no latency", sample: sample{download: good.download, upload: good.upload}, wantErr: true},
		{name: "speedtest-go N/A", sample: sample{latency: good.latency, download: -1, upload: good.upload}, wantErr: true},
		{name: "unmeasured download", sample: sample{latency: good.latency, upload: good.upload}, wantErr: true},
		{name: "unmeasured upload", sample: sample{latency: good.latency, download: good.download}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.sample.valid()
			if (err != nil) != tt.wantErr {
				t.Errorf("valid() = %v, want error %v", err, tt.wantErr)
			}
		})
	}
}
