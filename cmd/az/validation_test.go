package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRetrieveValidationArtifactPublishesVerifiedStreamAtomically(t *testing.T) {
	content := []byte("complete retained output")
	reference, digest := testValidationArtifactIdentity(content)
	destination := filepath.Join(t.TempDir(), "result.log")
	require.NoError(t, os.WriteFile(destination, []byte("existing"), 0o600))

	reader := testValidationArtifactReader(reference, digest, content, 5, nil)
	require.NoError(t, retrieveValidationArtifact(context.Background(), reference, destination, nil, reader))
	got, err := os.ReadFile(destination)
	require.NoError(t, err)
	assert.Equal(t, content, got)
}

func TestRetrieveValidationArtifactPreservesDestinationOnFailure(t *testing.T) {
	content := []byte("complete retained output")
	reference, digest := testValidationArtifactIdentity(content)
	for _, test := range []struct {
		name      string
		reference string
		mutate    func(int, *protocol.ValidationArtifactReadResponse) error
	}{
		{name: "invalid reference", reference: "not-an-artifact"},
		{name: "rpc failure", reference: reference, mutate: func(call int, _ *protocol.ValidationArtifactReadResponse) error {
			if call == 1 {
				return fmt.Errorf("connection lost")
			}
			return nil
		}},
		{name: "changed reference", reference: reference, mutate: func(call int, response *protocol.ValidationArtifactReadResponse) error {
			if call == 1 {
				response.Reference = "artifact:sha256/" + string(bytes.Repeat([]byte{'0'}, sha256.Size*2))
			}
			return nil
		}},
		{name: "changed total size", reference: reference, mutate: func(call int, response *protocol.ValidationArtifactReadResponse) error {
			if call == 1 {
				response.TotalSize++
			}
			return nil
		}},
		{name: "changed digest metadata", reference: reference, mutate: func(call int, response *protocol.ValidationArtifactReadResponse) error {
			if call == 1 {
				response.Digest = "sha256:" + string(bytes.Repeat([]byte{'0'}, sha256.Size*2))
			}
			return nil
		}},
		{name: "premature complete", reference: reference, mutate: func(call int, response *protocol.ValidationArtifactReadResponse) error {
			if call == 0 {
				response.Complete = true
			}
			return nil
		}},
		{name: "truncated stream with stable metadata", reference: reference, mutate: func(call int, response *protocol.ValidationArtifactReadResponse) error {
			if call == 1 {
				response.Content = response.Content[:len(response.Content)-1]
				response.NextOffset--
				response.Complete = true
			}
			return nil
		}},
		{name: "extended stream with stable metadata", reference: reference, mutate: func(call int, response *protocol.ValidationArtifactReadResponse) error {
			if call == 4 {
				response.Content = append(response.Content, 'x')
				response.NextOffset++
			}
			return nil
		}},
		{name: "final digest mismatch", reference: reference, mutate: func(_ int, response *protocol.ValidationArtifactReadResponse) error {
			if len(response.Content) > 0 {
				response.Content[0] ^= 1
			}
			return nil
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			destination := filepath.Join(dir, "result.log")
			require.NoError(t, os.WriteFile(destination, []byte("existing"), 0o600))
			err := retrieveValidationArtifact(context.Background(), test.reference, destination, nil, testValidationArtifactReader(reference, digest, content, 5, test.mutate))
			require.Error(t, err)
			got, readErr := os.ReadFile(destination)
			require.NoError(t, readErr)
			assert.Equal(t, []byte("existing"), got)
			assertNoValidationArtifactTemps(t, dir)
		})
	}
}

func TestRetrieveValidationArtifactPreservesDestinationOnStagedWriteFailure(t *testing.T) {
	content := []byte("complete retained output")
	reference, digest := testValidationArtifactIdentity(content)
	dir := t.TempDir()
	destination := filepath.Join(dir, "result.log")
	require.NoError(t, os.WriteFile(destination, []byte("existing"), 0o600))

	err := retrieveValidationArtifactWithStager(context.Background(), reference, destination, nil, testValidationArtifactReader(reference, digest, content, 5, nil), func(dir, pattern string) (validationArtifactStage, error) {
		file, createErr := os.CreateTemp(dir, pattern)
		return &failingValidationArtifactStage{File: file}, createErr
	})
	require.ErrorContains(t, err, "write staged validation artifact")
	got, readErr := os.ReadFile(destination)
	require.NoError(t, readErr)
	assert.Equal(t, []byte("existing"), got)
	assertNoValidationArtifactTemps(t, dir)
}

func TestRetrieveValidationArtifactPreservesDestinationOnStageDurabilityFailures(t *testing.T) {
	for _, failure := range []string{"sync", "close"} {
		t.Run(failure, func(t *testing.T) {
			content := []byte("complete retained output")
			reference, digest := testValidationArtifactIdentity(content)
			dir := t.TempDir()
			destination := filepath.Join(dir, "result.log")
			require.NoError(t, os.WriteFile(destination, []byte("existing"), 0o600))
			err := retrieveValidationArtifactWithStager(context.Background(), reference, destination, nil, testValidationArtifactReader(reference, digest, content, 5, nil), func(dir, pattern string) (validationArtifactStage, error) {
				file, createErr := os.CreateTemp(dir, pattern)
				return &failingValidationArtifactDurabilityStage{File: file, failure: failure}, createErr
			})
			require.ErrorContains(t, err, failure+" staged validation artifact")
			assertValidationArtifactDestinationUnchanged(t, dir, destination)
		})
	}
}

func TestRetrieveValidationArtifactPreservesDestinationOnPublishDurabilityFailures(t *testing.T) {
	for _, failure := range []string{"directory-open", "rename", "directory-sync", "directory-close", "backup-remove"} {
		t.Run(failure, func(t *testing.T) {
			content := []byte("complete retained output")
			reference, digest := testValidationArtifactIdentity(content)
			dir := t.TempDir()
			destination := filepath.Join(dir, "result.log")
			require.NoError(t, os.WriteFile(destination, []byte("existing"), 0o600))
			ops := defaultValidationArtifactFileOps()
			switch failure {
			case "directory-open":
				ops.openDirectory = func(string) (validationArtifactDirectory, error) { return nil, fmt.Errorf("open failed") }
			case "rename":
				ops.rename = func(string, string) error { return fmt.Errorf("rename failed") }
			case "directory-sync":
				ops.openDirectory = func(string) (validationArtifactDirectory, error) {
					return &failingValidationArtifactDirectory{failure: "sync"}, nil
				}
			case "directory-close":
				ops.openDirectory = func(string) (validationArtifactDirectory, error) {
					return &failingValidationArtifactDirectory{failure: "close"}, nil
				}
			case "backup-remove":
				removeCalls := 0
				ops.remove = func(path string) error {
					removeCalls++
					if removeCalls == 1 {
						return fmt.Errorf("remove failed")
					}
					return os.Remove(path)
				}
			}
			err := retrieveValidationArtifactWithOps(context.Background(), reference, destination, nil, testValidationArtifactReader(reference, digest, content, 5, nil), func(dir, pattern string) (validationArtifactStage, error) {
				return os.CreateTemp(dir, pattern)
			}, ops)
			require.Error(t, err)
			assertValidationArtifactDestinationUnchanged(t, dir, destination)
		})
	}
}

type failingValidationArtifactDurabilityStage struct {
	*os.File
	failure string
}

func (s *failingValidationArtifactDurabilityStage) Sync() error {
	if s.failure == "sync" {
		return fmt.Errorf("sync failed")
	}
	return s.File.Sync()
}

func (s *failingValidationArtifactDurabilityStage) Close() error {
	if s.failure == "close" {
		_ = s.File.Close()
		return fmt.Errorf("close failed")
	}
	return s.File.Close()
}

type failingValidationArtifactDirectory struct{ failure string }

func (d *failingValidationArtifactDirectory) Sync() error {
	if d.failure == "sync" {
		return fmt.Errorf("sync failed")
	}
	return nil
}

func (d *failingValidationArtifactDirectory) Close() error {
	if d.failure == "close" {
		return fmt.Errorf("close failed")
	}
	return nil
}

func assertValidationArtifactDestinationUnchanged(t *testing.T, dir, destination string) {
	t.Helper()
	got, err := os.ReadFile(destination)
	require.NoError(t, err)
	assert.Equal(t, []byte("existing"), got)
	assertNoValidationArtifactTemps(t, dir)
}

type failingValidationArtifactStage struct {
	*os.File
}

func (s *failingValidationArtifactStage) Write([]byte) (int, error) {
	return 0, fmt.Errorf("staged write failed")
}

func assertNoValidationArtifactTemps(t *testing.T, dir string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, ".az-validation-artifact-*"))
	require.NoError(t, err)
	assert.Empty(t, matches)
}

func TestRetrieveValidationArtifactBuffersStdoutUntilVerified(t *testing.T) {
	content := []byte("unverified output")
	reference, digest := testValidationArtifactIdentity(content)
	var stdout bytes.Buffer
	err := retrieveValidationArtifact(context.Background(), reference, "", &stdout, testValidationArtifactReader(reference, digest, content, 4, func(_ int, response *protocol.ValidationArtifactReadResponse) error {
		response.Content[0] ^= 1
		return nil
	}))
	require.ErrorContains(t, err, "digest mismatch")
	assert.Empty(t, stdout.Bytes())
}

func testValidationArtifactIdentity(content []byte) (string, string) {
	digest := fmt.Sprintf("%x", sha256.Sum256(content))
	return "artifact:sha256/" + digest, "sha256:" + digest
}

func testValidationArtifactReader(reference, digest string, content []byte, chunkSize int, mutate func(int, *protocol.ValidationArtifactReadResponse) error) validationArtifactReader {
	call := 0
	return func(_ context.Context, request protocol.ValidationArtifactReadRequest) (protocol.ValidationArtifactReadResponse, error) {
		start := request.Offset
		end := start + int64(chunkSize)
		if end > int64(len(content)) {
			end = int64(len(content))
		}
		response := protocol.ValidationArtifactReadResponse{
			Reference: reference, Digest: digest, Content: append([]byte(nil), content[start:end]...),
			Offset: start, NextOffset: end, TotalSize: int64(len(content)), Complete: end == int64(len(content)),
		}
		if mutate != nil {
			if err := mutate(call, &response); err != nil {
				return protocol.ValidationArtifactReadResponse{}, err
			}
		}
		call++
		return response, nil
	}
}
