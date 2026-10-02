package telemlineage

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Expected values come from the published Telem JS SDK, @telemai/sdk 0.2.2
// (fingerprint, sessionKey, eventNodeKey, snapshotNodeKey, buildMetadata and
// buildSnapshot), so every Telem client derives the same keys.

func TestKeysMatchJSSDK(t *testing.T) {
	cases := []struct {
		harness, conversation, window, message, toolCall string
		fingerprint, session, eventNode, snapshotNode    string
	}{
		{"omnara", "0192f5a0-0000-7000-8000-000000000001", "none", "0192f5a0-0000-7000-8000-00000000000a",
			"0192f5a0-0000-7000-8000-0000000000aa",
			"39e9503ec919750931d50946f6ce658b502e792e5da73ae0e8cd57d6e5ff03c4",
			"b1f97447-cf87-5f40-bc04-1bacf409733f", "0de06284-0052-5a9d-ac6d-1d50e5b1ad82",
			"a50a9904-8744-566f-a465-06ff979436dc"},
		{"omnara", "agent", "0192f5a0-0000-7000-8000-00000000c0de", "msg", "call",
			"0b198c9d4687b9dfe1ab45dbbb7040e4ba49d5c3aabb10879e27e58b1ee54f0d",
			"f29b3312-ad41-501d-8d84-7bdd074f13a4", "8ecd6b70-7537-5740-8164-49b0d8681efb",
			"246e2dba-08f8-5643-a2f6-d5697745fb45"},
		{"opencode", "ses_abc:def", "comp:1", "msg_1", "toolu_01",
			"0e4370870a25c3e94e7f72865d8aeeb76f35b8fb1ec0567cbba8465078ada4e5",
			"18216dc9-04ff-51d0-8a9a-1e136a3b2f7d", "1dbca5f0-837f-5a0e-b3ea-b3d6dfbda25f",
			"bec093f0-c1e1-50ef-8c12-5dd9f1e9bc2a"},
		{"omnara", "", "", "", "",
			"f9a74dc95287ebf4746404435c9298b582a1287cb919bddd09d37960ca0cb26d",
			"bd0dc3de-e0a6-5fd1-bc30-9398a957077e", "93394cb2-5652-522a-8b0c-1e6224581ab6",
			"b70a1c95-f5dc-543d-964c-499766710289"},
		// Lengths are in UTF-8 bytes, not characters.
		{"omnara", "会话-αβγ", "窗口", "メッセージ", "🔧call",
			"f8749ce6a9df615f7ce8f1df81e94b9ccbacbea14937d1a7c0ea506d107c7ed2",
			"f33faf66-0d00-53ac-b8a3-f4626a1c1a60", "5142110f-da36-5ecb-826f-ddc2c8e6dad4",
			"6a7f6566-839a-5522-9230-ffa5b6964adb"},
		// Length prefixes keep ambiguous concatenations apart.
		{"h", "12:foo:bar", "3:foo", "3:bar", "x",
			"43be0b2108ae011d31d7e7e3166dd574035895fa8c6320e045ae88c541a5559e",
			"378e161f-32a8-501e-817a-b4d6f961088f", "85caff8a-df08-517a-9325-1d55aebec4dd",
			"4e158be0-8ae6-59e6-8c6d-0e355e91f559"},
	}
	for _, c := range cases {
		session := sessionKey(c.harness, c.conversation, c.window)
		got := [...]string{
			fingerprint(c.harness, c.conversation),
			session,
			eventNodeKey(c.harness, session, c.message, c.toolCall),
			snapshotNodeKey(c.harness, c.conversation, c.message),
		}
		if want := [...]string{c.fingerprint, c.session, c.eventNode, c.snapshotNode}; got != want {
			t.Errorf("keys(%q, %q, %q, %q, %q) = %v, want %v",
				c.harness, c.conversation, c.window, c.message, c.toolCall, got, want)
		}
	}
}

// id spells the test ids the SDK vectors were generated with.
func id(n int) uuid.UUID {
	return uuid.MustParse(fmt.Sprintf("0192f5a0-0000-7000-8000-%012d", n))
}

func TestMetadataMatchesJSSDK(t *testing.T) {
	spawnedAt := func(value string) time.Time {
		at, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			t.Fatal(err)
		}
		return at
	}
	rootSpawn := Call{
		AgentID: id(1), ModelCallContextID: id(12), ToolCallID: id(121),
		CreatedAt: spawnedAt("2026-09-26T10:00:00.123Z"),
	}
	childSpawn := Call{
		AgentID: id(2), WindowID: id(22), ModelCallContextID: id(24), ToolCallID: id(241),
		CreatedAt: spawnedAt("2026-09-26T10:05:30Z"),
	}
	cases := []struct {
		name    string
		lineage Lineage
		want    string
	}{
		{
			name:    "top-level agent",
			lineage: Lineage{Call: Call{AgentID: id(1), ModelCallContextID: id(11), ToolCallID: id(111)}},
			want: `{"session_key":"b1f97447-cf87-5f40-bc04-1bacf409733f",` +
				`"fingerprint":"39e9503ec919750931d50946f6ce658b502e792e5da73ae0e8cd57d6e5ff03c4",` +
				`"node_key":"60977526-7d1f-52ea-869f-bac1be8a3f7e","parent_node_key":null,"ancestors":[],` +
				`"kind":"search"}`,
		},
		{
			name: "subagent",
			lineage: Lineage{
				Call:      Call{AgentID: id(2), WindowID: id(22), ModelCallContextID: id(21), ToolCallID: id(211)},
				Ancestors: []Call{rootSpawn},
			},
			want: `{"session_key":"c14da5d6-d4e4-523f-adb3-4235dc9dae36",` +
				`"fingerprint":"cc2154ef2685ec4d1494fc96d30edfe1e664daef46d3bec93f49e91da80f6728",` +
				`"node_key":"4b2eb18e-f7f4-55d3-8496-a5b88c9fea4e",` +
				`"parent_node_key":"1952e8b0-3de9-51af-9c91-0a0621110c8f","ancestors":[` +
				`{"session_key":"b1f97447-cf87-5f40-bc04-1bacf409733f",` +
				`"fingerprint":"39e9503ec919750931d50946f6ce658b502e792e5da73ae0e8cd57d6e5ff03c4",` +
				`"node_key":"1952e8b0-3de9-51af-9c91-0a0621110c8f","parent_node_key":null,` +
				`"spawned_at":"2026-09-26T10:00:00.123Z"}],"kind":"search"}`,
		},
		{
			name: "grandchild agent",
			lineage: Lineage{
				Call:      Call{AgentID: id(3), ModelCallContextID: id(31), ToolCallID: id(311)},
				Ancestors: []Call{rootSpawn, childSpawn},
			},
			want: `{"session_key":"b83c2269-0289-5019-ac63-e1855bf400ff",` +
				`"fingerprint":"ac53179f6ae6388dfc1cccf528ae8dd25fa7f818060d02d657502a5ff582752f",` +
				`"node_key":"0f6489ca-4566-5111-a4ee-83d154d5498c",` +
				`"parent_node_key":"72a43f37-b748-5c25-bb29-b2040667cd99","ancestors":[` +
				`{"session_key":"b1f97447-cf87-5f40-bc04-1bacf409733f",` +
				`"fingerprint":"39e9503ec919750931d50946f6ce658b502e792e5da73ae0e8cd57d6e5ff03c4",` +
				`"node_key":"1952e8b0-3de9-51af-9c91-0a0621110c8f","parent_node_key":null,` +
				`"spawned_at":"2026-09-26T10:00:00.123Z"},` +
				`{"session_key":"c14da5d6-d4e4-523f-adb3-4235dc9dae36",` +
				`"fingerprint":"cc2154ef2685ec4d1494fc96d30edfe1e664daef46d3bec93f49e91da80f6728",` +
				`"node_key":"72a43f37-b748-5c25-bb29-b2040667cd99",` +
				`"parent_node_key":"1952e8b0-3de9-51af-9c91-0a0621110c8f",` +
				`"spawned_at":"2026-09-26T10:05:30.000Z"}],"kind":"search"}`,
		},
	}
	for _, c := range cases {
		got, err := json.Marshal(Metadata(c.lineage, "search"))
		if err != nil {
			t.Fatal(err)
		}
		if !jsonEqual(t, got, []byte(c.want)) {
			t.Errorf("%s: Metadata() =\n%s\nwant\n%s", c.name, got, c.want)
		}
	}
}

func TestMetadataOfZeroLineageIsNil(t *testing.T) {
	if got := Metadata(Lineage{}, "search"); got != nil {
		t.Fatalf("Metadata(zero) = %v, want nil", got)
	}
}

func TestMetadataSendsGoalOnlyWhenSet(t *testing.T) {
	l := Lineage{Call: Call{AgentID: id(1), ToolCallID: id(111)}}
	if _, ok := Metadata(l, "search")["goal"]; ok {
		t.Fatal("Metadata sent a goal although none is set")
	}
	l.Goal = "Plan a 5-day Japan trip"
	if got := Metadata(l, "search")["goal"]; got != l.Goal {
		t.Fatalf("goal = %v, want %q", got, l.Goal)
	}
}

func jsonEqual(t *testing.T, a, b []byte) bool {
	t.Helper()
	var left, right any
	if json.Unmarshal(a, &left) != nil || json.Unmarshal(b, &right) != nil {
		t.Fatalf("invalid JSON: %s / %s", a, b)
	}
	return reflect.DeepEqual(left, right)
}
