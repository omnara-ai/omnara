package wideevent

import "testing"

func BenchmarkScrubLogString(b *testing.B) {
	for _, value := range []string{
		"018f7f6c-0314-7cbb-82f3-229f16533776", "ListContextEvents", "completed", "123456",
		"provider failed: token=secret https://example.test/path?key=secret",
	} {
		b.Run(value, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = ScrubLogString(value)
			}
		})
	}
}
