package testtiming

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
)

type goTestEvent struct {
	Action  string  `json:"Action"`
	Package string  `json:"Package"`
	Test    string  `json:"Test"`
	Elapsed float64 `json:"Elapsed"`
	Output  string  `json:"Output"`
}

type EventCollector struct {
	mu             sync.Mutex
	raw            io.Writer
	partial        []byte
	packages       map[string]Duration
	tests          map[string]Duration
	outputs        map[string]*strings.Builder
	cachedPackages map[string]bool
	failures       []Failure
	invalid        int
}

func NewEventCollector(raw io.Writer) *EventCollector {
	return &EventCollector{raw: raw, packages: map[string]Duration{}, tests: map[string]Duration{}, outputs: map[string]*strings.Builder{}, cachedPackages: map[string]bool{}}
}

func (c *EventCollector) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n, err := c.raw.Write(p)
	if err != nil {
		return n, err
	}
	if n != len(p) {
		return n, io.ErrShortWrite
	}
	c.partial = append(c.partial, p...)
	for {
		i := bytes.IndexByte(c.partial, '\n')
		if i < 0 {
			break
		}
		line := append([]byte(nil), c.partial[:i]...)
		c.partial = c.partial[i+1:]
		c.consume(line)
	}
	return len(p), nil
}

func (c *EventCollector) Finish() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.partial) > 0 {
		c.consume(c.partial)
		c.partial = nil
	}
}

func (c *EventCollector) consume(line []byte) {
	if len(line) == 0 {
		return
	}
	var event goTestEvent
	if err := json.Unmarshal(line, &event); err != nil {
		c.invalid++
		return
	}
	key := event.Package + "::" + event.Test
	if event.Output != "" {
		b := c.outputs[key]
		if b == nil {
			b = &strings.Builder{}
			c.outputs[key] = b
		}
		b.WriteString(event.Output)
		if event.Test == "" && strings.Contains(event.Output, "(cached)") {
			c.cachedPackages[event.Package] = true
		}
	}
	if event.Action != "pass" && event.Action != "fail" && event.Action != "skip" {
		return
	}
	if event.Test == "" {
		c.packages[event.Package] = Duration{Name: event.Package, Seconds: event.Elapsed, Action: event.Action, Cached: c.cachedPackages[event.Package]}
	} else {
		c.tests[key] = Duration{Name: key, Seconds: event.Elapsed, Action: event.Action}
	}
	if event.Action == "fail" {
		output := ""
		if b := c.outputs[key]; b != nil {
			output = strings.TrimSpace(b.String())
		}
		c.failures = append(c.failures, Failure{Package: event.Package, Test: event.Test, Output: output})
	}
	delete(c.outputs, key)
}

func (c *EventCollector) Results() (packages, tests []Duration, failures []Failure, invalid int) {
	packages = []Duration{}
	tests = []Duration{}
	failures = []Failure{}
	for _, d := range c.packages {
		packages = append(packages, d)
	}
	for _, d := range c.tests {
		packageName, _, _ := strings.Cut(d.Name, "::")
		d.Cached = c.cachedPackages[packageName]
		tests = append(tests, d)
	}
	sortDurations(packages)
	sortDurations(tests)
	failures = append(failures, c.failures...)
	slices.SortFunc(failures, func(a, b Failure) int { return strings.Compare(a.Package+"::"+a.Test, b.Package+"::"+b.Test) })
	return packages, tests, failures, c.invalid
}

func sortDurations(items []Duration) {
	slices.SortFunc(items, func(a, b Duration) int {
		if a.Seconds > b.Seconds {
			return -1
		}
		if a.Seconds < b.Seconds {
			return 1
		}
		return strings.Compare(a.Name, b.Name)
	})
}

func ParseEvents(r io.Reader) (packages, tests []Duration, failures []Failure, invalid int, err error) {
	collector := NewEventCollector(io.Discard)
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		collector.consume(scanner.Bytes())
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, nil, 0, fmt.Errorf("scan test events: %w", err)
	}
	packages, tests, failures, invalid = collector.Results()
	return packages, tests, failures, invalid, nil
}
