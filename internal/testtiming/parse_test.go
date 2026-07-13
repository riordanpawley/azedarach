package testtiming

import (
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseEventsPreservesAllFailuresAndSortsDurations(t *testing.T) {
	input := strings.Join([]string{
		`{"Action":"run","Package":"example/a","Test":"TestSlow"}`,
		`{"Action":"output","Package":"example/a","Test":"TestSlow","Output":"slow detail\n"}`,
		`{"Action":"fail","Package":"example/a","Test":"TestSlow","Elapsed":3.2}`,
		`{"Action":"fail","Package":"example/a","Elapsed":3.5}`,
		`not-json-but-preserved-by-the-runner`,
		`{"Action":"pass","Package":"example/b","Test":"TestTieB","Elapsed":1.2}`,
		`{"Action":"pass","Package":"example/b","Test":"TestTieA","Elapsed":1.2}`,
		`{"Action":"output","Package":"example/b","Output":"ok  example/b  (cached)\\n"}`,
		`{"Action":"pass","Package":"example/b","Elapsed":1.4}`,
	}, "\n")

	packages, tests, failures, invalid, err := ParseEvents(strings.NewReader(input))
	require.NoError(t, err)
	assert.Equal(t, 1, invalid)
	require.Len(t, packages, 2)
	assert.Equal(t, "example/a", packages[0].Name)
	assert.False(t, packages[0].Cached)
	assert.True(t, packages[1].Cached)
	require.Len(t, tests, 3)
	assert.Equal(t, []string{"example/a::TestSlow", "example/b::TestTieA", "example/b::TestTieB"}, []string{tests[0].Name, tests[1].Name, tests[2].Name})
	assert.False(t, tests[0].Cached)
	assert.True(t, tests[1].Cached)
	assert.True(t, tests[2].Cached)
	require.Len(t, failures, 2)
	assert.Equal(t, "example/a", failures[0].Package)
	assert.Empty(t, failures[0].Test)
	assert.Equal(t, "TestSlow", failures[1].Test)
	assert.Equal(t, "slow detail", failures[1].Output)
}

func TestEventCollectorPreservesRawBytesAcrossSplitWrites(t *testing.T) {
	var raw strings.Builder
	collector := NewEventCollector(&raw)
	_, err := collector.Write([]byte(`{"Action":"pass","Package":"example/a",`))
	require.NoError(t, err)
	_, err = collector.Write([]byte(`"Test":"TestA","Elapsed":0.4}` + "\n"))
	require.NoError(t, err)
	collector.Finish()

	assert.Equal(t, `{"Action":"pass","Package":"example/a","Test":"TestA","Elapsed":0.4}`+"\n", raw.String())
	_, tests, failures, invalid := collector.Results()
	require.Len(t, tests, 1)
	assert.Equal(t, "example/a::TestA", tests[0].Name)
	assert.Empty(t, failures)
	assert.NotNil(t, failures, "machine-readable empty collections must encode as arrays, not null")
	assert.Zero(t, invalid)
}

func TestEventCollectorRejectsShortRawWrites(t *testing.T) {
	collector := NewEventCollector(shortWriter{})
	n, err := collector.Write([]byte("complete-event"))
	assert.Equal(t, 1, n)
	assert.ErrorIs(t, err, io.ErrShortWrite)
	_, _, _, invalid := collector.Results()
	assert.Zero(t, invalid, "an event not preserved in full must not be parsed as authoritative")
}

type shortWriter struct{}

func (shortWriter) Write([]byte) (int, error) { return 1, nil }
