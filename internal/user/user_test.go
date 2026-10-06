package user

import (
	"strings"
	"testing"
	"time"

	"github.com/EasySB-Team/EasySB/internal/state"
)

var testNow = time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

func TestNewGeneratesCredentialFields(t *testing.T) {
	u := New("alice", []string{
		state.ProtoTUIC,
		state.ProtoAnyTLS,
		state.ProtoVLESSReality,
		state.ProtoHysteria2,
		state.ProtoVMessWSTLS,
	}, testNow)

	if u.Token == "" || len(u.Token) != 16 {
		t.Fatalf("token = %q, want 16 characters", u.Token)
	}
	if !u.Enabled {
		t.Fatal("a new account must start enabled")
	}
	if !u.LastReset.Equal(testNow) {
		t.Fatalf("last_reset = %v, want %v", u.LastReset, testNow)
	}
	if got := strings.Join(u.Protocols, ","); got != "anytls,hysteria2,tuic,vless-reality,vmess-ws-tls" {
		t.Fatalf("protocols not in canonical order: %s", got)
	}

	cases := map[string]struct {
		wantUUID     bool
		wantPassword bool
	}{
		state.ProtoAnyTLS:       {wantPassword: true},
		state.ProtoHysteria2:    {wantPassword: true},
		state.ProtoTUIC:         {wantUUID: true, wantPassword: true},
		state.ProtoVLESSReality: {wantUUID: true},
		state.ProtoVMessWSTLS:   {wantUUID: true},
	}
	for key, want := range cases {
		cred := u.Credential(key)
		if (cred.UUID != "") != want.wantUUID {
			t.Fatalf("%s uuid = %q, want set=%v", key, cred.UUID, want.wantUUID)
		}
		if (cred.Password != "") != want.wantPassword {
			t.Fatalf("%s password = %q, want set=%v", key, cred.Password, want.wantPassword)
		}
	}
}

func TestSelectKeepsExistingCredentials(t *testing.T) {
	u := New("alice", []string{state.ProtoTUIC}, testNow)
	original := u.Credential(state.ProtoTUIC)

	if !u.Deselect(state.ProtoTUIC) {
		t.Fatal("deselect reported no change")
	}
	if u.Selects(state.ProtoTUIC) {
		t.Fatal("protocol still selected after deselect")
	}
	if !u.Select(state.ProtoTUIC) {
		t.Fatal("re-selecting a deselected protocol reported no change")
	}
	if got := u.Credential(state.ProtoTUIC); got != original {
		t.Fatalf("credential rotated on re-select: %+v, want %+v", got, original)
	}
	if u.Select(state.ProtoTUIC) {
		t.Fatal("selecting twice must report no change")
	}
	if u.Select("nonsense") {
		t.Fatal("an unknown protocol key must be rejected")
	}
}

func TestStatus(t *testing.T) {
	base := func() User {
		return User{Name: "a", Token: "t", Enabled: true, Protocols: []string{state.ProtoAnyTLS}}
	}
	cases := []struct {
		name string
		edit func(*User)
		want Status
	}{
		{"active", func(*User) {}, StatusActive},
		{"disabled wins", func(u *User) { u.Enabled = false }, StatusDisabled},
		{"expired", func(u *User) { u.ExpireAt = testNow.Add(-time.Hour) }, StatusExpired},
		{"expires in the future", func(u *User) { u.ExpireAt = testNow.Add(time.Hour) }, StatusActive},
		{"over quota", func(u *User) { u.QuotaBytes = 100; u.UsedBytes = 100 }, StatusQuota},
		{"under quota", func(u *User) { u.QuotaBytes = 100; u.UsedBytes = 99 }, StatusActive},
		{"quota ignored when unlimited", func(u *User) { u.QuotaBytes = 0; u.UsedBytes = 1 << 40 }, StatusActive},
		{"disabled beats expired", func(u *User) {
			u.Enabled = false
			u.ExpireAt = testNow.Add(-time.Hour)
		}, StatusDisabled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u := base()
			tc.edit(&u)
			if got := u.Status(testNow); got != tc.want {
				t.Fatalf("status = %s, want %s", got, tc.want)
			}
			if u.Usable(testNow) != (tc.want == StatusActive) {
				t.Fatalf("usable disagrees with status %s", tc.want)
			}
		})
	}
}

func TestQuotaArithmetic(t *testing.T) {
	u := User{QuotaBytes: 1000, UsedBytes: 250}
	if u.Unlimited() {
		t.Fatal("quota 1000 must not be unlimited")
	}
	if got := u.Remaining(); got != 750 {
		t.Fatalf("remaining = %d, want 750", got)
	}
	if got := u.Percent(); got != 25 {
		t.Fatalf("percent = %d, want 25", got)
	}
	u.UsedBytes = 2000
	if got := u.Remaining(); got != 0 {
		t.Fatalf("remaining = %d, want 0", got)
	}
	if got := u.Percent(); got != 100 {
		t.Fatalf("percent = %d, want 100", got)
	}
	unlimited := User{}
	if !unlimited.Unlimited() || unlimited.Remaining() != 0 || unlimited.Percent() != 0 {
		t.Fatal("zero quota must behave as unlimited")
	}
}

func TestAddUsageIgnoresNegativeDeltas(t *testing.T) {
	u := User{}
	u.AddUsage(100, 50)
	u.AddUsage(-1, -1)
	if u.UploadBytes != 100 || u.DownloadBytes != 50 || u.UsedBytes != 150 {
		t.Fatalf("counters = %d/%d/%d, want 100/50/150", u.UploadBytes, u.DownloadBytes, u.UsedBytes)
	}
}

func TestResetIfNewMonth(t *testing.T) {
	u := User{UsedBytes: 500, UploadBytes: 200, DownloadBytes: 300,
		LastReset: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}
	if u.ResetIfNewMonth(time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)) {
		t.Fatal("same month must not reset")
	}
	if u.UsedBytes != 500 {
		t.Fatal("same month must keep the counters")
	}
	if !u.ResetIfNewMonth(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatal("new month must reset")
	}
	if u.UsedBytes != 0 || u.UploadBytes != 0 || u.DownloadBytes != 0 {
		t.Fatalf("counters not cleared: %d/%d/%d", u.UsedBytes, u.UploadBytes, u.DownloadBytes)
	}
	if u.LastReset.Month() != time.October {
		t.Fatalf("last_reset month = %v, want October", u.LastReset.Month())
	}

	blank := User{UsedBytes: 10}
	if !blank.ResetIfNewMonth(testNow) {
		t.Fatal("an account without a period start must reset")
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name string
		user User
		ok   bool
	}{
		{"valid", User{Name: "alice", Token: "abc"}, true},
		{"chinese name", User{Name: "张三", Token: "abc"}, true},
		{"empty name", User{Name: "", Token: "abc"}, false},
		{"blank name", User{Name: "  ", Token: "abc"}, false},
		{"control character", User{Name: "a\nb", Token: "abc"}, false},
		{"too long", User{Name: strings.Repeat("龙", 41), Token: "abc"}, false},
		{"missing token", User{Name: "alice", Token: ""}, false},
		{"padded token", User{Name: "alice", Token: " abc"}, false},
		{"negative quota", User{Name: "alice", Token: "abc", QuotaBytes: -1}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.user.Validate()
			if tc.ok && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestEnsureCredentialsDropsUnknownProtocols(t *testing.T) {
	u := User{
		Name:      "alice",
		Token:     "abc",
		Protocols: []string{state.ProtoAnyTLS},
		Credentials: map[string]Credentials{
			"leaked-protocol": {Password: "x"},
		},
	}
	u.EnsureCredentials()
	if _, ok := u.Credentials["leaked-protocol"]; ok {
		t.Fatal("an unknown protocol key survived EnsureCredentials")
	}
	if u.Credential(state.ProtoAnyTLS).Password == "" {
		t.Fatal("the selected protocol has no password")
	}
}
