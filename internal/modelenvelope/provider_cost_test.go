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

func TestSumProviderReportedCostUSD(t *testing.T) {
	for _, test := range []struct {
		name  string
		costs []string
		want  ProviderReportedCostUSD
		ok    bool
	}{
		{
			name:  "free routing plus upstream inference",
			costs: []string{"0", "0.0000076"},
			want:  "0.0000076",
			ok:    true,
		},
		{
			name:  "routing fee plus upstream inference",
			costs: []string{"0.95", "19"},
			want:  "19.95",
			ok:    true,
		},
		{
			name:  "exact fractional addition",
			costs: []string{"0.1", "0.2"},
			want:  "0.3",
			ok:    true,
		},
		{
			name:  "rounds only the final total",
			costs: []string{"0.0000000000000004", "0.0000000000000004"},
			want:  "0.000000000000001",
			ok:    true,
		},
		{
			name:  "database precision overflow",
			costs: []string{"999999999999999.999999999999999", "0.000000000000001"},
		},
		{name: "missing component", costs: []string{"1", ""}},
		{name: "no components"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, ok := SumProviderReportedCostUSD(test.costs...)
			if got != test.want || ok != test.ok {
				t.Fatalf("SumProviderReportedCostUSD(%q) = %q, %v; want %q, %v", test.costs, got, ok, test.want, test.ok)
			}
		})
	}
}
