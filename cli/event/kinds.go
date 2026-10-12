package event

import "fmt"

// kindInfo names a kind and the NIP that defines it.
type kindInfo struct {
	Name string `json:"name"`
	NIP  string `json:"nip,omitempty"`
}

var kindNames = map[int]kindInfo{
	0:     {"profile metadata", "01"},
	1:     {"short text note", "10"},
	3:     {"follow list", "02"},
	4:     {"encrypted direct message", "04"},
	5:     {"deletion request", "09"},
	6:     {"repost", "18"},
	7:     {"reaction", "25"},
	8:     {"badge award", "58"},
	13:    {"seal", "59"},
	14:    {"direct message", "17"},
	16:    {"generic repost", "18"},
	20:    {"picture", "68"},
	21:    {"video", "71"},
	22:    {"short video", "71"},
	40:    {"channel creation", "28"},
	41:    {"channel metadata", "28"},
	42:    {"channel message", "28"},
	1018:  {"poll response", "88"},
	1059:  {"gift wrap", "59"},
	1063:  {"file metadata", "94"},
	1068:  {"poll", "88"},
	1111:  {"comment", "22"},
	1222:  {"voice message", "A0"},
	1244:  {"voice message reply", "A0"},
	1311:  {"live chat message", "53"},
	1617:  {"git patch", "34"},
	1618:  {"git pull request", "34"},
	1619:  {"git pull request update", "34"},
	1621:  {"git issue", "34"},
	1630:  {"git status: open", "34"},
	1631:  {"git status: applied", "34"},
	1632:  {"git status: closed", "34"},
	1633:  {"git status: draft", "34"},
	1984:  {"report", "56"},
	1985:  {"label", "32"},
	7000:  {"job feedback", "90"},
	8000:  {"relay member added", "43"},
	8001:  {"relay member removed", "43"},
	9000:  {"group: put user", "29"},
	9001:  {"group: remove user", "29"},
	9002:  {"group: edit metadata", "29"},
	9005:  {"group: delete event", "29"},
	9007:  {"group: create", "29"},
	9008:  {"group: delete", "29"},
	9009:  {"group: create invite", "29"},
	9010:  {"group: update pins", "29"},
	9021:  {"group: join request", "29"},
	9022:  {"group: leave request", "29"},
	9734:  {"zap request", "57"},
	9735:  {"zap receipt", "57"},
	9802:  {"highlight", "84"},
	10000: {"mute list", "51"},
	10001: {"pinned notes", "51"},
	10002: {"relay list", "65"},
	10009: {"group list", "29"},
	10063: {"blossom server list", "B7"},
	10312: {"room presence", "53"},
	13194: {"wallet info", "47"},
	13534: {"relay membership list", "43"},
	22242: {"relay login", "42"},
	23194: {"wallet request", "47"},
	23195: {"wallet response", "47"},
	24133: {"remote signing", "46"},
	24242: {"blossom authorization", "B7"},
	27235: {"HTTP auth", "98"},
	28934: {"relay join request", "43"},
	28935: {"relay invite", "43"},
	28936: {"relay leave request", "43"},
	30000: {"follow set", "51"},
	30008: {"profile badges", "58"},
	30009: {"badge definition", "58"},
	30023: {"long-form article", "23"},
	30024: {"long-form draft", "23"},
	30311: {"live event", "53"},
	30312: {"meeting space", "53"},
	30313: {"meeting room", "53"},
	30617: {"git repository", "34"},
	30618: {"git repository state", "34"},
	31922: {"calendar event (date)", "52"},
	31923: {"calendar event (time)", "52"},
	33534: {"relay role", "43"},
	34235: {"addressable video", "71"},
	34236: {"addressable short video", "71"},
	35521: {"identity connection", "IC"},
	35522: {"attestation", "IC"},
	39000: {"group metadata", "29"},
	39001: {"group admins", "29"},
	39002: {"group members", "29"},
	39003: {"group roles", "29"},
	39004: {"group live participants", "29"},
	39005: {"group pinned events", "29"},
	39701: {"web bookmark", "B0"},
}

// describeKind names a kind, falling back to its NIP-01 class.
func describeKind(kind int) kindInfo {
	if k, ok := kindNames[kind]; ok {
		return k
	}
	switch {
	case kind >= 5000 && kind < 6000:
		return kindInfo{"job request", "90"}
	case kind >= 6000 && kind < 7000:
		return kindInfo{"job result", "90"}
	case kind >= 10000 && kind < 20000:
		return kindInfo{Name: "replaceable"}
	case kind >= 20000 && kind < 30000:
		return kindInfo{Name: "ephemeral"}
	case kind >= 30000 && kind < 40000:
		return kindInfo{Name: "addressable"}
	}
	return kindInfo{Name: "unknown"}
}

func (k kindInfo) String() string {
	if k.NIP == "" {
		return k.Name
	}
	return fmt.Sprintf("%s (NIP-%s)", k.Name, k.NIP)
}
