package modelenvelope

import "testing"

func TestParseProviderReportedCostUSD(t *testing.T) {
	tests := []struct {
		raw  string
		want ProviderReportedCostUSD
		ok   bool
	}{
		{raw: "0", want: "0", ok: true},
		{raw: "0.000012500", want: "0.0000125", ok: true},
		{raw: "1.25e-5", want: "0.0000125", ok: true},
		{raw: "1.0672500000000001e-05", want: "0.0000106725", ok: true},
		{raw: "1e2", want: "100", ok: true},
		{raw: "100.000", want: "100", ok: true},
		{raw: "999999999999999", want: "999999999999999", ok: true},
		{raw: "0.000000000000001", want: "0.000000000000001", ok: true},
		{raw: "0.0000000000000005", want: "0.000000000000001", ok: true},
		{raw: "0.0000000000000004", want: "0", ok: true},
		{raw: "-0", want: "0", ok: true},
		{raw: "-0.0e10", want: "0", ok: true},
		{raw: "-0.01"},
		{raw: "01"},
		{raw: ".1"},
		{raw: "1."},
		{raw: "1e"},
		{raw: "999999999999999.9999999999999995"},
		{raw: "1000000000000000"},
	}
	for _, test := range tests {
		t.Run(test.raw, func(t *testing.T) {
			got, ok := ParseProviderReportedCostUSD(test.raw)
			if got != test.want || ok != test.ok {
				t.Fatalf("ParseProviderReportedCostUSD(%q) = %q, %v; want %q, %v", test.raw, got, ok, test.want, test.ok)
			}
		})
	}
}
