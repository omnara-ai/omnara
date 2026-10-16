package filesearch

import (
	"bytes"
	"regexp"
	"strings"
)

func Compile(options Options) (*regexp.Regexp, error) {
	patterns := make([]string, len(options.Patterns))
	for i, pattern := range options.Patterns {
		if options.Fixed {
			pattern = regexp.QuoteMeta(pattern)
		}
		patterns[i] = "(?:" + pattern + ")"
	}
	expression := strings.Join(patterns, "|")
	if options.Word {
		expression = `\b(?:` + expression + `)\b`
	}
	if options.Line {
		expression = `^(?:` + expression + `)$`
	}
	flags := ""
	if options.IgnoreCase {
		flags += "i"
	}
	if options.Multiline {
		flags += "m"
	}
	if flags != "" {
		expression = "(?" + flags + ")" + expression
	}
	return regexp.Compile(expression)
}

type block struct {
	first, last int
	start, end  int
}

func (c *Collector) Search(path string, content []byte, re *regexp.Regexp, options Options) error {
	if len(content) == 0 {
		return nil
	}
	starts := []int{0}
	for i, b := range content {
		if b == '\n' && i+1 < len(content) {
			starts = append(starts, i+1)
		}
	}
	lineEnd := func(line int) int {
		if line+1 < len(starts) {
			return starts[line+1] - 1
		}
		return len(bytes.TrimSuffix(content, []byte{'\n'}))
	}
	lineOf := func(offset int) int {
		low, high := 0, len(starts)-1
		for low < high {
			mid := (low + high + 1) / 2
			if starts[mid] <= offset {
				low = mid
			} else {
				high = mid - 1
			}
		}
		return low
	}
	var blocks []block
	if options.Multiline {
		for _, match := range re.FindAllIndex(content, -1) {
			first, last := lineOf(match[0]), lineOf(max(match[0], match[1]-1))
			if n := len(blocks); n > 0 && first <= blocks[n-1].last {
				blocks[n-1].last = max(blocks[n-1].last, last)
				continue
			}
			blocks = append(blocks, block{first: first, last: last,
				start: match[0] - starts[first], end: match[1] - starts[first]})
		}
		if options.Invert {
			covered := blocks
			blocks = nil
			for line := range starts {
				for len(covered) > 0 && covered[0].last < line {
					covered = covered[1:]
				}
				if len(covered) == 0 || line < covered[0].first {
					blocks = append(blocks, block{first: line, last: line})
				}
			}
		}
	} else {
		for line := range starts {
			match := re.FindIndex(content[starts[line]:lineEnd(line)])
			if (match != nil) == options.Invert {
				continue
			}
			found := block{first: line, last: line}
			if match != nil {
				found.start, found.end = match[0], match[1]
			}
			blocks = append(blocks, found)
		}
	}
	switch options.Mode {
	case "-l":
		if len(blocks) == 0 {
			return nil
		}
		return c.AddFile(path, nil)
	case "-c":
		var count uint64
		for _, found := range blocks {
			count += uint64(found.last - found.first + 1)
		}
		if count == 0 {
			return nil
		}
		return c.AddFile(path, &count)
	}
	text := func(first, last int) string {
		return string(content[starts[first]:lineEnd(last)])
	}
	next := 0
	for i, found := range blocks {
		for line := max(next, found.first-options.Before); line < found.first; line++ {
			if err := c.AddLine(path, line+1, text(line, line), 0, 0, false); err != nil {
				return err
			}
		}
		if err := c.AddLine(path, found.first+1, text(found.first, found.last), found.start, found.end, true); err != nil {
			return err
		}
		next = found.last + 1
		stop := min(found.last+options.After, len(starts)-1)
		if i+1 < len(blocks) {
			stop = min(stop, blocks[i+1].first-1)
		}
		for ; next <= stop; next++ {
			if err := c.AddLine(path, next+1, text(next, next), 0, 0, false); err != nil {
				return err
			}
		}
	}
	return nil
}
