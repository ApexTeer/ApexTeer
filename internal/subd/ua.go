package subd

import (
	"net/http"
	"strings"

	"github.com/EasySBTeam/EasySB/internal/subscribe"
)

// clientFromRequest picks the document format from the client's User-Agent,
// because every client hard-codes the format it can parse and none of them sends
// a "give me this format" parameter. An explicit ?client= overrides the guess
// for clients whose User-Agent is unrecognised or spoofed.
func clientFromRequest(r *http.Request) subscribe.Client {
	if asked := strings.TrimSpace(r.URL.Query().Get("client")); asked != "" {
		for _, c := range subscribe.Clients {
			if strings.EqualFold(asked, string(c)) {
				return c
			}
		}
	}
	return clientFromUserAgent(r.UserAgent())
}

// uaRules maps a User-Agent marker to the format its client reads. The order
// matters: sing-box is checked first, then the Clash family, and everything else
// falls through to the base64 share-link document, which is what v2rayN,
// Shadowrocket, passwall, passwall2 and homeproxy parse.
var uaRules = []struct {
	marker string
	client subscribe.Client
}{
	{"sing-box", subscribe.ClientSingBox},
	{"sfi/", subscribe.ClientSingBox},
	{"sfa/", subscribe.ClientSingBox},
	{"sfm/", subscribe.ClientSingBox},
	{"clash", subscribe.ClientMihomo},
	{"mihomo", subscribe.ClientMihomo},
	{"stash", subscribe.ClientMihomo},
	{"meta", subscribe.ClientMihomo},
}

func clientFromUserAgent(ua string) subscribe.Client {
	ua = strings.ToLower(ua)
	for _, rule := range uaRules {
		if strings.Contains(ua, rule.marker) {
			return rule.client
		}
	}
	return subscribe.ClientV2Ray
}
